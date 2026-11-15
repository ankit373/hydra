// SPDX-License-Identifier: MIT

// Package drift says when a pass rate changed, and what it has been since.
// Bayesian online change-point detection (Adams and MacKay 2007), plus idle
// steps that still carry the hazard, so a source nobody calls can recover.
package drift

import "math"

const (
	// DefaultHazard is the prior chance of a change per step, the setting that
	// both left a broken head and came back to a fixed one in simulation.
	DefaultHazard = 1.0 / 1000
	// MaxSegments bounds memory and the cost of a step.
	MaxSegments = 500
	// confidence is the posterior mass a younger segment needs before it counts
	// as a change. The most probable one alone flips on a short run of fails.
	confidence = 0.99
)

// Segment is one guess at when the current rate began: its weight, the step it
// started on, and the passes and fails seen since.
type Segment struct {
	Weight float64
	Start  int64
	Passes int
	Fails  int
}

// Detector holds every guess about the current segment, oldest first, and the
// last one is always the fresh segment with nothing seen yet. Plain fields, so
// a caller can persist it as is.
type Detector struct {
	Hazard   float64
	A0, B0   float64
	Steps    int64
	Segments []Segment
	// Best is the Start of the current segment, and Moved whether the last
	// step moved it to a younger one.
	Best  int64
	Moved bool
}

// New starts a detector whose segments begin at a Beta(a0, b0) prior.
func New(a0, b0 float64) *Detector {
	return &Detector{Hazard: DefaultHazard, A0: a0, B0: b0, Segments: []Segment{{Weight: 1}}}
}

// Observe is one BOCPD step with a Beta-Bernoulli predictive.
func (d *Detector) Observe(pass bool) {
	var change float64
	for i := range d.Segments {
		s := &d.Segments[i]
		hits := float64(s.Fails) + d.B0
		if pass {
			hits = float64(s.Passes) + d.A0
		}
		p := s.Weight * hits / (float64(s.Passes+s.Fails) + d.A0 + d.B0)
		change += p * d.Hazard
		s.Weight = p * (1 - d.Hazard)
		if pass {
			s.Passes++
		} else {
			s.Fails++
		}
	}
	d.Steps++
	d.Segments = append(d.Segments, Segment{Weight: change, Start: d.Steps})
	if len(d.Segments) > MaxSegments {
		d.fold()
	}
	d.normalize()
	d.settle()
}

// Idle is k steps with nothing observed. Each keeps the hazard and drops the
// likelihood, so in closed form (1-H)^k stays put and the rest starts afresh.
func (d *Detector) Idle(k int) {
	if k <= 0 {
		return
	}
	keep := math.Pow(1-d.Hazard, float64(k))
	for i := range d.Segments {
		d.Segments[i].Weight *= keep
	}
	// The fresh segment still has nothing seen, so every segment the idle steps
	// would start is the same guess as it and merges into it.
	d.Segments[len(d.Segments)-1].Weight += 1 - keep
	d.Steps += int64(k)
	d.normalize()
	d.settle()
}

// Since is the passes and fails of the current segment: the record since the
// last change.
func (d *Detector) Since() (passes, fails int) {
	for _, s := range d.Segments {
		if s.Start == d.Best {
			return s.Passes, s.Fails
		}
	}
	return 0, 0
}

// Changed reports whether the last step found a change.
func (d *Detector) Changed() bool { return d.Moved }

// fold merges the lowest-weight segment into an older neighbour, keeping the
// longer record, so a long stable segment is never the one cut. The fresh
// segment is never folded.
func (d *Detector) fold() {
	low := 0
	if d.Segments[0].Start == d.Best {
		low = 1
	}
	for i := low + 1; i < len(d.Segments)-1; i++ {
		if d.Segments[i].Start != d.Best && d.Segments[i].Weight < d.Segments[low].Weight {
			low = i
		}
	}
	into := low - 1
	if low == 0 {
		into = 1
	}
	d.Segments[into].Weight += d.Segments[low].Weight
	d.Segments = append(d.Segments[:low], d.Segments[low+1:]...)
}

func (d *Detector) normalize() {
	sum := 0.0
	for _, s := range d.Segments {
		sum += s.Weight
	}
	for i := range d.Segments {
		d.Segments[i].Weight /= sum
	}
}

// best is the most probable segment; on a tie the older one, so equal weights
// never read as a change.
func (d *Detector) best() int {
	b := 0
	for i := 1; i < len(d.Segments); i++ {
		if d.Segments[i].Weight > d.Segments[b].Weight {
			b = i
		}
	}
	return b
}

// settle moves the current segment to the most probable one: back to an older
// one freely, which retracts a false alarm, forward only with confidence.
func (d *Detector) settle() {
	start := d.Segments[d.best()].Start
	d.Moved = false
	if start <= d.Best {
		d.Best = start
		return
	}
	mass := 0.0
	for _, s := range d.Segments {
		if s.Start > d.Best {
			mass += s.Weight
		}
	}
	if mass >= confidence {
		d.Best, d.Moved = start, true
	}
}
