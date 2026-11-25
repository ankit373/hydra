// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ankit373/hydra/internal/entropy"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/idle"
	"github.com/ankit373/hydra/internal/pricing"
	"github.com/ankit373/hydra/internal/serve"
)

// Swap modes, [idle] swap in config.toml.
const (
	swapOff      = "off"
	swapColdOnly = "cold_only"
	swapAuto     = "auto"
)

// defaultTurnsLeft is how many more requests to expect when a conversation has
// no history of pauses to read it from. A policy value, not a measurement.
const defaultTurnsLeft = 4

// maxRequestTimes bounds the request times kept per conversation, and
// maxConversations how many conversations a long-running server remembers.
const (
	maxRequestTimes  = 200
	maxConversations = 1000
)

// validSwap reports whether mode is one of the three; empty is the default.
func validSwap(mode string) error {
	switch mode {
	case "", swapOff, swapColdOnly, swapAuto:
		return nil
	}
	return fmt.Errorf("[idle] swap = %q: use %s, %s or %s", mode, swapOff, swapColdOnly, swapAuto)
}

// compactor sends a staged pack in place of the turns it covers, when the
// request's leading messages are exactly those turns and the cache math says it pays.
type compactor struct {
	mode  string
	dir   string
	now   func() time.Time
	rates func(head string) idle.Rates

	mu       sync.Mutex
	requests map[string][]time.Time
}

func newCompactor(mode string) *compactor {
	if mode == "" {
		mode = swapColdOnly
	}
	return &compactor{mode: mode, dir: idleDir(), now: time.Now, rates: pinnedRates, requests: map[string][]time.Time{}}
}

var (
	priceOnce sync.Once
	priceDB   *pricing.DB
)

// pinnedRates are the cache rates of a head the client named, from the pricing
// data. An unpinned request has no head yet, so its rates are unknown.
func pinnedRates(head string) idle.Rates {
	if head == "" {
		return idle.Rates{}
	}
	priceOnce.Do(func() { priceDB = pricing.Load() })
	p, ok := priceDB.ModelPrice(head)
	if !ok {
		return idle.Rates{}
	}
	return idle.RatesFor(p)
}

// since is the time since conv's last request, and whether this process saw one.
// It also records this request. A conversation first seen here has an unknown
// cache, which reads as warm.
func (c *compactor) since(conv string) (time.Duration, []time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	times, ok := c.requests[conv]
	if !ok && len(c.requests) >= maxConversations {
		c.forgetOldest()
	}
	var d time.Duration
	if len(times) > 0 {
		d = now.Sub(times[len(times)-1])
	}
	known := len(times) > 0
	prior := slices.Clone(times)
	times = append(times, now)
	if len(times) > maxRequestTimes {
		times = times[len(times)-maxRequestTimes:]
	}
	c.requests[conv] = times
	return d, prior, known
}

// forgetOldest drops the conversation whose last request is oldest. The caller
// holds c.mu. Linear, and only when a new conversation arrives at the cap.
func (c *compactor) forgetOldest() {
	var oldest string
	var at time.Time
	for conv, times := range c.requests {
		if last := times[len(times)-1]; oldest == "" || last.Before(at) {
			oldest, at = conv, last
		}
	}
	delete(c.requests, oldest)
}

// apply returns the messages to send and what was swapped, nil when nothing was.
// Any doubt, a changed turn, a partial pack, a tool pair cut in two, sends the
// request as it came.
func (c *compactor) apply(msgs []executor.Message, conv, head string) ([]executor.Message, *serve.Compacted) {
	if c == nil || conv == "" || c.mode == swapOff || idle.Restored(c.dir, conv) {
		return msgs, nil
	}
	quiet, prior, seen := c.since(conv)
	staged, n, err := idle.LatestStaged(c.dir, conv)
	if err != nil || staged == nil || staged.Verdict.Partial || !staged.Verdict.Pass || staged.Pack.Covers.From != 0 {
		return msgs, nil
	}
	end, covered := coveredPrefix(msgs, staged.Pack.Covers)
	if end == 0 || idle.CoverHash(covered) != staged.Pack.Covers.Hash {
		return msgs, nil
	}
	if end < len(msgs) && msgs[end].Role == "tool" {
		return msgs, nil
	}
	rules, state := staged.Pack.Blocks()
	before, after := entropy.TokenEstimate(turnsText(covered)), entropy.TokenEstimate(rules+state)
	warm := !seen || idle.Warm(quiet)
	if !c.pays(float64(before), float64(after), warm, quiet, prior, head) {
		return msgs, nil
	}
	out := make([]executor.Message, 0, len(msgs)-end+3)
	if len(msgs) > 0 && (msgs[0].Role == "system" || msgs[0].Role == "developer") {
		out = append(out, msgs[0])
	}
	if rules != "" {
		out = append(out, executor.Message{Role: "system", Content: idle.RulesHeading + rules})
	}
	out = append(out, executor.Message{Role: "system", Content: idle.StateHeading + state})
	out = append(out, msgs[end:]...)
	c.markUsed(conv, n)
	return out, &serve.Compacted{Pack: idle.PackID(conv, n), ProbeAgree: staged.Verdict.ProbeAgree,
		Probes: staged.Verdict.Probes, TokensBefore: before, TokensAfter: after}
}

// markUsed counts a swap for `hyctl context packs`. A failure to count never
// fails the request.
func (c *compactor) markUsed(conv string, n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = idle.MarkUsed(c.dir, conv, n, c.now())
}

// pays applies the swap mode: cold_only swaps only a cold cache, auto asks ShouldSwap.
func (c *compactor) pays(before, after float64, warm bool, quiet time.Duration, prior []time.Time, head string) bool {
	if c.mode == swapColdOnly {
		return !warm && after < before
	}
	turns := idle.TurnsLeft(prior, quiet, defaultTurnsLeft)
	ok, _ := idle.ShouldSwap(before, after, turns, c.rates(head), warm)
	return ok
}

// coveredPrefix maps the request's messages to turns, skipping empty ones as
// the transcript does, and returns the first cover.To of them with the index
// after the last message they came from. 0 when the request has fewer.
func coveredPrefix(msgs []executor.Message, cover idle.Covers) (int, []idle.Turn) {
	var turns []idle.Turn
	for i, m := range msgs {
		if m.Content == "" {
			continue
		}
		turns = append(turns, idle.Turn{Role: m.Role, Text: m.Content})
		if len(turns) == cover.To {
			return i + 1, turns
		}
	}
	return 0, nil
}

func turnsText(ts []idle.Turn) string {
	var b strings.Builder
	for _, t := range ts {
		b.WriteString(t.Text)
		b.WriteByte('\n')
	}
	return b.String()
}
