// SPDX-License-Identifier: MIT

// Package sketch summarises a distribution in bounded space.
//
// Quantiles have no fixed-size sufficient statistic, which is why latency is
// the one metric people keep raw rows for. They should not: 200k latencies cost
// 1.6 MB raw and answer p99 exactly, while this answers it within half a
// percent in a few KB that never grow.
//
// Buckets are logarithmic, so the error bound is *relative* rather than on
// rank. That distinction is the whole point on skewed data like model latency,
// where a rank-error sketch can be arbitrarily wrong in the tail you care
// about. Sketches also merge, which is what lets two machines combine
// statistics without either shipping a trace.
package sketch

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

// DefaultAlpha is the relative-error target. At 1% a log-normal latency
// population resolves p50 through p99 inside 1% in a few hundred buckets.
const DefaultAlpha = 0.01

// Bucket count grows with the log of the range covered, not with the number of
// observations, so a cap is only reached by data spanning an implausible range:
// at DefaultAlpha, DefaultMaxBuckets represents a ratio of about 6e17 between
// the largest and smallest value. Past the cap the guarantee becomes
// directional, see collapse.

// DefaultMaxBuckets caps memory. A sketch must not be able to grow without
// bound because one caller logged a pathological value; past the cap the
// lowest buckets collapse, which degrades accuracy for the smallest values
// and leaves the tail, the part anyone asks about, intact.
const DefaultMaxBuckets = 2048

// ErrAlphaMismatch reports a merge between sketches with different error
// targets. Their buckets mean different things, so combining them would
// silently produce a sketch whose stated accuracy is a lie.
var ErrAlphaMismatch = errors.New("sketch: cannot merge sketches with different alpha")

// Sketch is a mergeable relative-error quantile sketch.
// The zero value is unusable; use New.
type Sketch struct {
	Alpha      float64         `json:"alpha"`
	MaxBuckets int             `json:"max_buckets"`
	Zeros      int64           `json:"zeros"`     // exact count of v == 0
	Negatives  int64           `json:"negatives"` // counted, never bucketed
	Buckets    map[int64]int64 `json:"buckets"`
	N          int64           `json:"n"`
	MinIndex   int64           `json:"min_index"` // lowest live bucket after any collapse
}

// New returns an empty sketch with the given relative-error target. A
// non-positive or >=1 alpha falls back to DefaultAlpha rather than producing a
// sketch whose gamma is undefined or infinite.
func New(alpha float64) *Sketch {
	if !(alpha > 0 && alpha < 1) {
		alpha = DefaultAlpha
	}
	return &Sketch{
		Alpha:      alpha,
		MaxBuckets: DefaultMaxBuckets,
		Buckets:    map[int64]int64{},
		MinIndex:   math.MaxInt64,
	}
}

func (s *Sketch) gamma() float64 { return (1 + s.Alpha) / (1 - s.Alpha) }

// index maps a positive value to its bucket.
func (s *Sketch) index(v float64) int64 {
	return int64(math.Ceil(math.Log(v) / math.Log(s.gamma())))
}

// value maps a bucket back to its representative, the midpoint of the bucket
// in the sense that guarantees the relative-error bound.
func (s *Sketch) value(i int64) float64 {
	g := s.gamma()
	return 2 * math.Pow(g, float64(i)) / (g + 1)
}

// Add records one observation. NaN and +Inf are dropped rather than bucketed:
// there is no index for them, and letting one in would corrupt every later
// quantile. Negative values are counted separately so a caller can see that
// something wrong reached a metric that should never be negative.
func (s *Sketch) Add(v float64) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return
	}
	s.N++
	switch {
	case v == 0:
		s.Zeros++
	case v < 0:
		s.Negatives++
	default:
		if s.Buckets == nil {
			s.Buckets = map[int64]int64{}
		}
		i := s.index(v)
		s.Buckets[i]++
		if i < s.MinIndex {
			s.MinIndex = i
		}
		s.collapse()
	}
}

// collapse folds the lowest buckets together once the cap is exceeded.
//
// Directional by design: a collapsed bucket merges into its neighbour above, so
// small values are overstated while every quantile above the collapsed region
// keeps its bound. That is the right trade for latency and cost, where the
// question is always about the tail.
func (s *Sketch) collapse() {
	for len(s.Buckets) > s.MaxBuckets {
		keys := make([]int64, 0, len(s.Buckets))
		for k := range s.Buckets {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(a, b int) bool { return keys[a] < keys[b] })
		lo, next := keys[0], keys[1]
		s.Buckets[next] += s.Buckets[lo]
		delete(s.Buckets, lo)
		s.MinIndex = next
	}
}

// Count reports how many observations the sketch has seen, including zeros and
// negatives.
func (s *Sketch) Count() int64 { return s.N }

// Quantile returns the estimated value at q in [0,1]. An empty sketch returns
// 0: there is no answer, and callers check Count before presenting a number.
//
// Zeros and negatives are ordered before every bucketed value, so a population
// that is mostly zero reports zero for the low quantiles rather than skipping
// to the smallest positive observation.
func (s *Sketch) Quantile(q float64) float64 {
	if s.N == 0 {
		return 0
	}
	if q < 0 {
		q = 0
	}
	if q > 1 {
		q = 1
	}
	target := q * float64(s.N-1)
	var seen float64

	if s.Negatives > 0 {
		seen += float64(s.Negatives)
		if target < seen {
			return math.NaN() // present but unrepresentable; do not invent a value
		}
	}
	if s.Zeros > 0 {
		seen += float64(s.Zeros)
		if target < seen {
			return 0
		}
	}
	keys := make([]int64, 0, len(s.Buckets))
	for k := range s.Buckets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(a, b int) bool { return keys[a] < keys[b] })
	for _, k := range keys {
		seen += float64(s.Buckets[k])
		if target < seen {
			return s.value(k)
		}
	}
	if len(keys) == 0 {
		return 0
	}
	return s.value(keys[len(keys)-1])
}

// Merge folds other into s. Two sketches of the same alpha merge into one as
// accurate as a single sketch of the union, the property that makes fleet-wide
// statistics possible without moving any raw data.
func (s *Sketch) Merge(other *Sketch) error {
	if other == nil {
		return nil
	}
	if math.Abs(s.Alpha-other.Alpha) > 1e-12 {
		return fmt.Errorf("%w: %v vs %v", ErrAlphaMismatch, s.Alpha, other.Alpha)
	}
	if s.Buckets == nil {
		s.Buckets = map[int64]int64{}
	}
	for k, v := range other.Buckets {
		s.Buckets[k] += v
		if k < s.MinIndex {
			s.MinIndex = k
		}
	}
	s.Zeros += other.Zeros
	s.Negatives += other.Negatives
	s.N += other.N
	s.collapse()
	return nil
}

// Clone returns a deep copy, so a caller can merge into a snapshot without
// mutating the sketch a reader still holds.
func (s *Sketch) Clone() *Sketch {
	c := *s
	c.Buckets = make(map[int64]int64, len(s.Buckets))
	for k, v := range s.Buckets {
		c.Buckets[k] = v
	}
	return &c
}
