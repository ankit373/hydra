// SPDX-License-Identifier: MIT

package util

import (
	"os"
	"strconv"
	"sync"
)

const (
	// DefaultMaxBytes is 33 MB — matches Claude Code's EndTruncatingAccumulator default.
	DefaultMaxBytes = 1 << 25

	truncationMarker = "\n\n[… output truncated — limit exceeded …]"
)

// Accumulator is a bounded, goroutine-safe io.Writer that caps captured output
// at MaxBytes. Once the cap is hit it stops appending and sets Truncated=true.
// Wire it anywhere you capture subprocess or streaming LLM output.
type Accumulator struct {
	mu        sync.Mutex
	buf       []byte
	maxBytes  int
	total     int // bytes written before truncation
	truncated bool
}

// NewAccumulator returns an Accumulator capped at maxBytes.
// Pass 0 to use DefaultMaxBytes.
// The HYDRA_MAX_OUTPUT_BYTES environment variable overrides both.
func NewAccumulator(maxBytes int) *Accumulator {
	if env := os.Getenv("HYDRA_MAX_OUTPUT_BYTES"); env != "" {
		if n, err := strconv.Atoi(env); err == nil && n > 0 {
			maxBytes = n
		}
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	return &Accumulator{maxBytes: maxBytes}
}

// Write implements io.Writer. Once the buffer is full, additional bytes are
// counted in Total but not stored; a truncation marker is appended once.
func (a *Accumulator) Write(p []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	n := len(p)
	a.total += n

	if a.truncated {
		return n, nil
	}

	remaining := a.maxBytes - len(a.buf)
	if remaining <= 0 {
		a.markTruncated()
		return n, nil
	}

	if len(p) <= remaining {
		a.buf = append(a.buf, p...)
	} else {
		a.buf = append(a.buf, p[:remaining]...)
		a.markTruncated()
	}

	return n, nil
}

func (a *Accumulator) markTruncated() {
	a.truncated = true
	a.buf = append(a.buf, []byte(truncationMarker)...)
}

// String returns the accumulated output (possibly truncated).
func (a *Accumulator) String() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return string(a.buf)
}

// Len returns the number of bytes currently stored. When truncation has
// occurred this includes the length of the truncation marker itself, so
// Len() may exceed maxBytes. Do not use Len() as a budget guard — use
// TotalBytes() to know how many bytes were actually written by the source.
func (a *Accumulator) Len() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.buf)
}

// TotalBytes returns the total bytes written, regardless of truncation.
func (a *Accumulator) TotalBytes() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.total
}

// Truncated reports whether output was cut off.
func (a *Accumulator) Truncated() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.truncated
}

// Reset clears all state so the Accumulator can be reused.
// The underlying byte slice is retained (resliced to zero length) to avoid
// re-allocation on the next use. If the previous capture was large (e.g. a
// 33 MB subprocess output) and the Accumulator will not be reused, set it to
// nil instead of calling Reset to release the backing memory.
func (a *Accumulator) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.buf = a.buf[:0]
	a.total = 0
	a.truncated = false
}
