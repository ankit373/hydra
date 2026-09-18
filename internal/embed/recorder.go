// SPDX-License-Identifier: MIT

package embed

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ankit373/hydra/internal/policy"
)

// QueueDepth bounds work waiting to be embedded. Past it Record drops rather
// than blocks: a dispatch must not wait on observability, which is already the
// rule every runlog append follows.
const QueueDepth = 64

// Recorder embeds and stores text without the caller waiting for either.
//
// The whole point of the type is the guarantee that Record returns at once,
// whatever the embedding model is doing, so the dispatch it observes keeps its
// latency and its result.
type Recorder struct {
	emb Embedder
	st  *Store
	ch  chan item

	stopOnce sync.Once
	done     chan struct{}

	dropped atomic.Int64
	stored  atomic.Int64
	failed  atomic.Int64
}

type item struct {
	spanID string
	text   string
}

// NewRecorder starts a recorder. A nil store or an unavailable embedder yields
// one that accepts and discards, so callers need no branch for the common case
// of a machine with no embedding model.
func NewRecorder(emb Embedder, st *Store) *Recorder {
	r := &Recorder{emb: emb, st: st, done: make(chan struct{})}
	if emb == nil || !emb.Available() || st == nil {
		close(r.done)
		return r
	}
	r.ch = make(chan item, QueueDepth)
	go r.run()
	return r
}

// Record queues text for embedding under a span id. Never blocks, never errors:
// a dropped embedding is counted and reported, not raised at a caller who has
// nothing useful to do about it.
func (r *Recorder) Record(spanID, text string) {
	if r.ch == nil || len(spanID) != spanIDLen || text == "" {
		return
	}
	select {
	case r.ch <- item{spanID: spanID, text: text}:
	default:
		r.dropped.Add(1)
	}
}

func (r *Recorder) run() {
	defer close(r.done)
	for it := range r.ch {
		r.one(it)
	}
}

// one redacts, embeds and stores a single item.
//
// Redaction happens here rather than in Record for two reasons: it costs the
// caller nothing, and it is the last point before the text leaves the process,
// so no path reaches the model without passing through it.
func (r *Recorder) one(it item) {
	clean, _ := policy.Redact(it.text)

	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()

	vec, err := r.emb.Embed(ctx, clean)
	if err != nil {
		r.failed.Add(1)
		return
	}
	if err := r.st.Put(it.spanID, vec); err != nil {
		r.failed.Add(1)
		return
	}
	r.stored.Add(1)
}

// Close stops accepting work and waits for what is queued, up to timeout.
// Safe to call more than once.
func (r *Recorder) Close(timeout time.Duration) {
	r.stopOnce.Do(func() {
		if r.ch != nil {
			close(r.ch)
		}
	})
	select {
	case <-r.done:
	case <-time.After(timeout):
	}
}

// Counts reports what the recorder did. Dropped is the queue overflowing,
// failed is the model or the store refusing, and the two are separate because
// the remedies are: one is load, the other is a broken embedder.
func (r *Recorder) Counts() (stored, dropped, failed int64) {
	return r.stored.Load(), r.dropped.Load(), r.failed.Load()
}

// Cosine is the similarity between two vectors, in [-1,1].
//
// Returns 0 for a mismatched or zero-magnitude pair rather than NaN: a caller
// comparing against a threshold reads NaN as "not similar" only by accident,
// and every comparison with NaN is false including the one guarding the branch.
func Cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
