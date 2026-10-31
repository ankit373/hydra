// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"log"

	"github.com/ankit373/hydra/internal/cache"
	"github.com/ankit373/hydra/internal/egress"
	"github.com/ankit373/hydra/internal/embed"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/runid"
	"github.com/ankit373/hydra/internal/runlog"
)

// answerCache opens the cache at most once per Dispatcher, with its own
// embedder.
//
// Its own, rather than the recall store's: caching answers and keeping a
// searchable history are separate decisions, and someone who turned on only
// the first should still get near matches. The embedder persists nothing here
// beyond the cache entry the user already opted into.
func (d *Dispatcher) answerCache() (*cache.Store, embed.Embedder) {
	d.cacheOnce.Do(func() {
		st, err := cache.Open(d.cfg)
		if err != nil {
			log.Printf("⚠️  answer cache unavailable (%v), every dispatch runs a head", err)
			return
		}
		d.answers = st
		if st != nil {
			d.answerEmb = embed.Resolve(d.heads, d.cfg.EmbedModel)
		}
	})
	return d.answers, d.answerEmb
}

// fromCache answers a prompt from an earlier dispatch, or reports nothing.
//
// The exact match is tried before anything is embedded, so the common case
// costs a hash and the embedding call is only paid when the store might hold a
// near match worth finding.
// The second result says whether the cache was consulted at all, which is what
// separates a miss from a dispatch the cache was never allowed to answer. Only
// the first belongs in a hit rate; counting the second would blame the cache
// for the PII rule.
func (d *Dispatcher) fromCache(ctx context.Context, prompt string, opts Options, class *policy.Classification) (cache.Outcome, bool) {
	st, emb := d.answerCache()
	if st == nil {
		return cache.Outcome{}, false
	}
	if ok, _ := cache.Servable(admission(opts, class)); !ok {
		return cache.Outcome{}, false
	}

	var vec []float32
	if emb != nil && emb.Available() {
		if v, err := emb.Embed(ctx, cache.Normalize(prompt)); err == nil {
			vec = v
		}
	}
	return st.Lookup(prompt, vec, cache.Threshold(d.cfg, opts.Enum)), true
}

// recordLookup folds a consulted lookup into the tallies. Misses included, or
// the hit rate is one divided by itself and always reads 100%.
func (d *Dispatcher) recordLookup(out cache.Outcome) {
	if st, _ := d.answerCache(); st != nil {
		st.Record(out)
	}
}

// admission is the single reading of whether this dispatch may touch the
// cache. One derivation, because the lookup and the store have to agree: a
// rule applied on one side only lets a prompt into the store that can never be
// served from it, or worse, the other way around.
func admission(opts Options, class *policy.Classification) cache.Request {
	req := cache.Request{Pinned: opts.Head != "", NoCache: opts.NoCache}
	if class != nil {
		req.PII, req.Injection = class.PII, class.FlagReason != ""
	}
	return req
}

// cacheResult is a hit dressed as a dispatch outcome, with the head that
// actually produced the answer named rather than invented.
//
// Token counts stay zero: nothing was consumed this time, and reporting the
// original call's counts again would double every total that sums them.
func cacheResult(hit *cache.Hit) *Result {
	return &Result{
		Output: hit.Response,
		Head:   provider.Head{ID: hit.Head, Name: hit.Head},
		Cache:  hit,
		Response: &executor.Response{
			Output: hit.Response,
			Model:  hit.Model,
		},
	}
}

// logCacheHit records the hit as its own kind of event.
//
// No cost row and no dispatch row, which is the whole propensity question:
// internal/ope weights a logged row by the probability the router chose that
// head, and on a hit the router chose nothing. A row with no action would
// either need an invented propensity or divide by zero, and both corrupt every
// estimate computed afterwards (#605). The event still lands in the run log,
// so the run is not silent about what answered it.
func logCacheHit(rl *runlog.Logger, taskID, span string, hit *cache.Hit) {
	_ = rl.Append(runlog.Event{
		Kind: runlog.KindCacheHit, TaskID: taskID, SpanID: span,
		ParentSpanID: runlog.SpanIDFor(taskID),
		Head:         hit.Head, Detail: hit.Head,
		Meta: map[string]any{
			"similarity":  hit.Similarity,
			"exact":       hit.Exact,
			"age_seconds": int64(hit.Age.Seconds()),
			"avoided_usd": hit.CostUSD,
		},
	})
}

// remember stores a successful answer for a later dispatch.
//
// Refused for a response carrying credential-shaped content: the egress gate
// already classified it, and a cache is the one place where returning it again
// is a decision rather than an accident. Failures are logged and dropped,
// since a cache that cannot write must not fail work that already succeeded.
func (d *Dispatcher) remember(ctx context.Context, prompt string, opts Options, class *policy.Classification, r *Result, costUSD float64) {
	st, emb := d.answerCache()
	if st == nil || r.Output == "" {
		return
	}
	if r.OutputProvenance.Sens >= egress.Secret {
		return
	}
	// The same admission rules as the lookup, from the same classification.
	// Deciding them once per direction is how a prompt that may never be
	// served from the cache ends up inside it anyway.
	if ok, _ := cache.Servable(admission(opts, class)); !ok {
		return
	}

	e := cache.Entry{
		Prompt: prompt, Response: r.Output,
		Head: r.Head.ID, Model: r.Response.Model,
		Enum: opts.Enum, Domain: routingDomain(opts), CostUSD: costUSD,
	}
	var vec []float32
	if emb != nil && emb.Available() {
		if v, err := emb.Embed(ctx, cache.Normalize(prompt)); err == nil {
			vec = v
		}
	}
	if err := st.PutVec(e, vec); err != nil {
		log.Printf("⚠️  answer not cached: %v", err)
	}
}

// serveCached hands back a stored answer, records that it was used, and logs
// the hit. The only path on which a dispatch returns without a head running.
func (d *Dispatcher) serveCached(opts Options, out cache.Outcome) *Result {
	rl := runlog.New(runid.ResolveRun(opts.RunID))
	taskID := runid.ResolveTask(opts.TaskID)
	_ = rl.Append(runlog.Event{
		Kind: runlog.KindTaskStarted, TaskID: taskID,
		SpanID: runlog.SpanIDFor(taskID), Detail: opts.Enum,
	})
	logCacheHit(rl, taskID, runlog.NewSpanID(), &out.Hit)

	// No handoff: the a2a clock's actor is a head that acted, and nothing
	// acted here. Ticking it for a cache hit would invent an event the causal
	// ordering would then have to explain.
	return cacheResult(&out.Hit)
}
