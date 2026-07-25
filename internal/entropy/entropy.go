// SPDX-License-Identifier: MIT

// Package entropy measures the useful information in a context window, not just
// its length. Manifesto Law 5: useful_tokens = length × signal_density. A big
// noisy window can carry less usable context than a small dense one, so
// compaction should trigger on falling signal density — not token count alone.
package entropy

import (
	"bytes"
	"compress/gzip"
)

// SignalDensity estimates ρ ∈ (0,1]: how information-dense a context is, using
// the gzip compression ratio as a computable proxy for entropy per byte.
// Redundant, boilerplate, or repetitive text compresses to a small fraction
// (low ρ); varied, information-rich text resists compression (high ρ). Empty
// input has no signal (0).
func SignalDensity(text string) float64 {
	if len(text) == 0 {
		return 0
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = zw.Write([]byte(text))
	_ = zw.Close()

	// gzip has a fixed header/footer (~18 bytes); subtract it so tiny inputs
	// aren't scored as artificially dense.
	const overhead = 18
	compressed := buf.Len() - overhead
	if compressed < 1 {
		compressed = 1
	}
	ratio := float64(compressed) / float64(len(text))
	if ratio > 1 {
		ratio = 1
	}
	if ratio < 0 {
		ratio = 0
	}
	return ratio
}

// TokenEstimate approximates token count as bytes/4 (the same heuristic the agy
// executor uses). It is a proxy, not a real tokenizer.
func TokenEstimate(text string) int { return len(text) / 4 }

// UsefulTokens is length × signal density: the information actually carried by a
// context, in token-equivalents.
func UsefulTokens(text string) float64 {
	return float64(TokenEstimate(text)) * SignalDensity(text)
}

// Snapshot captures a context window's size and quality at a point in time.
type Snapshot struct {
	Tokens       int     // estimated total tokens
	Density      float64 // ρ
	UsefulTokens float64 // Tokens × ρ
}

// Measure builds a Snapshot for a context string.
func Measure(text string) Snapshot {
	rho := SignalDensity(text)
	return Snapshot{
		Tokens:       TokenEstimate(text),
		Density:      rho,
		UsefulTokens: float64(TokenEstimate(text)) * rho,
	}
}

// DefaultMinDensity is the signal-density floor below which a context is mostly
// noise and should be compacted regardless of length.
const DefaultMinDensity = 0.35

// Governor decides when to recommend compaction based on signal density, not
// just length.
type Governor struct {
	// MinDensity is the ρ floor; below it, compaction is recommended. Zero uses
	// DefaultMinDensity.
	MinDensity float64
}

// Recommendation is the governor's verdict for a context.
type Recommendation struct {
	Compact bool
	Reason  string
	Snap    Snapshot
}

// Assess evaluates a single context snapshot against the density floor.
func (g Governor) Assess(text string) Recommendation {
	floor := g.MinDensity
	if floor <= 0 {
		floor = DefaultMinDensity
	}
	snap := Measure(text)
	if snap.Density < floor {
		return Recommendation{
			Compact: true,
			Reason:  "signal density below floor — window is mostly redundant context",
			Snap:    snap,
		}
	}
	return Recommendation{Compact: false, Reason: "context is dense enough", Snap: snap}
}

// Regressed reports whether growing the context from prev to cur added length
// but not useful information — i.e. the window got bigger and *less* useful, the
// clearest signal to compact.
func Regressed(prev, cur Snapshot) bool {
	return cur.Tokens > prev.Tokens && cur.UsefulTokens <= prev.UsefulTokens
}
