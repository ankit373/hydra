// SPDX-License-Identifier: MIT

package retrieve

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/ankit373/hydra/internal/embed"
)

// QueueDepth bounds work waiting to be indexed. Past it Record drops rather
// than blocks: a dispatch must not wait on observability.
const QueueDepth = 64

// Recorder is the single write path for both halves.
//
// One entry point and one drain, rather than a caller remembering to feed two
// stores that must hold the same documents. The lexical write is cheap and the
// embedding is not, so they have separate workers and the slow one cannot stall
// the one that works everywhere.
type Recorder struct {
	ix  *Index
	emb *embed.Recorder
	ch  chan item

	stopOnce sync.Once
	done     chan struct{}

	indexed atomic.Int64
	dropped atomic.Int64
	failed  atomic.Int64
}

type item struct{ docID, text string }

// NewRecorder starts the write path for a searcher. A nil searcher yields one
// that accepts and discards, so callers need no branch for capture being off.
func NewRecorder(s *Searcher) *Recorder {
	r := &Recorder{done: make(chan struct{})}
	if s == nil || s.ix == nil {
		close(r.done)
		return r
	}
	r.ix = s.ix
	r.emb = embed.NewRecorder(s.emb, s.vec)
	r.ch = make(chan item, QueueDepth)
	go r.run()
	return r
}

// Record queues a prompt for indexing and embedding. Never blocks.
func (r *Recorder) Record(docID, text string) {
	if r.ch == nil || docID == "" || text == "" {
		return
	}
	select {
	case r.ch <- item{docID: docID, text: text}:
	default:
		r.dropped.Add(1)
	}
	if r.emb != nil {
		r.emb.Record(docID, text)
	}
}

func (r *Recorder) run() {
	defer close(r.done)
	for it := range r.ch {
		// Add redacts before it tokenises, so the secret never becomes a term.
		if err := r.ix.Add(it.docID, it.text); err != nil {
			r.failed.Add(1)
			continue
		}
		r.indexed.Add(1)
	}
}

// Close stops accepting work and waits for both halves, up to timeout.
func (r *Recorder) Close(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	r.stopOnce.Do(func() {
		if r.ch != nil {
			close(r.ch)
		}
	})
	select {
	case <-r.done:
	case <-time.After(time.Until(deadline)):
	}
	if r.emb != nil {
		r.emb.Close(time.Until(deadline))
	}
}

// Counts reports what the lexical half did. Dropped is the queue overflowing,
// failed is the index refusing, and the two are separate because one is load
// and the other is broken.
func (r *Recorder) Counts() (indexed, dropped, failed int64) {
	return r.indexed.Load(), r.dropped.Load(), r.failed.Load()
}

// Embeddings reports the vector half's own counts.
func (r *Recorder) Embeddings() (stored, dropped, failed int64) {
	if r.emb == nil {
		return 0, 0, 0
	}
	return r.emb.Counts()
}
