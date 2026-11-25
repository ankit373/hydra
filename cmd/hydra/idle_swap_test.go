// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/idle"
	"github.com/ankit373/hydra/internal/serve"
)

const swapConv = "conv-a"

func swapMessages() []executor.Message {
	return []executor.Message{
		{Role: "system", Content: "Never promise a refund over $200 without a manager."},
		{Role: "user", Content: "I was charged twice on invoice 4417. " + strings.Repeat("History line. ", 200)},
		{Role: "assistant", Content: "We will refund one charge because both lines are the same invoice."},
		{Role: "user", Content: "Did that go through?"},
	}
}

// stagePack writes a staged pack covering the first three messages.
func stagePack(t *testing.T, msgs []executor.Message, v idle.Verdict) {
	t.Helper()
	var turns []idle.Turn
	for _, m := range msgs[:3] {
		turns = append(turns, idle.Turn{Role: m.Role, Text: m.Content})
	}
	p := idle.Pack{
		State: idle.State{
			Goal:        "refund the double charge",
			Constraints: []idle.Quote{{Text: msgs[0].Content, Ref: "s"}},
			Decisions:   []idle.Decision{{What: "refund one charge", Why: "both lines are the same invoice", Ref: "a1"}},
		},
		Covers: idle.Covers{To: 3, Hash: idle.CoverHash(turns)},
	}
	raw, _ := json.Marshal(idle.Staged{Pack: p, Verdict: v})
	dir := filepath.Join(idleDir(), swapConv)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pack-1.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

var passed = idle.Verdict{Pass: true, Probes: 10, ProbeAgree: 9}

// coldCompactor has already seen a request, ten minutes ago.
func coldCompactor(mode string) *compactor {
	c := newCompactor(mode)
	now := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	c.apply(swapMessages(), swapConv, "")
	c.now = func() time.Time { return now.Add(10 * time.Minute) }
	return c
}

func TestServe_SwapsOnlyTheCoveredSpan(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	msgs := swapMessages()
	stagePack(t, msgs, passed)
	got, c := coldCompactor(swapColdOnly).apply(msgs, swapConv, "")
	if c == nil {
		t.Fatal("a cold cache with a staged pack was not swapped")
	}
	if len(got) != 4 || got[0].Content != msgs[0].Content || got[3].Content != msgs[3].Content {
		t.Fatalf("messages %+v, want the system message first and the last turn untouched", got)
	}
	for _, m := range got[1:3] {
		if m.Role != "system" {
			t.Fatalf("block %+v, want a system block", m)
		}
	}
	if !strings.Contains(got[1].Content, msgs[0].Content) || !strings.Contains(got[2].Content, "refund one charge, because both lines are the same invoice") {
		t.Fatalf("blocks %q and %q, want the rule word for word and the decision with its reason", got[1].Content, got[2].Content)
	}
	if strings.Contains(got[2].Content, "History line") || c.TokensAfter >= c.TokensBefore {
		t.Fatal("the covered turns were sent as well, or the pack is not smaller")
	}
}

func TestServe_MismatchedSpanSendsOriginal(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	msgs := swapMessages()
	stagePack(t, msgs, passed)
	for name, edit := range map[string]func([]executor.Message) []executor.Message{
		"a covered turn changed":           func(m []executor.Message) []executor.Message { m[1].Content += "!"; return m },
		"fewer turns than the pack covers": func(m []executor.Message) []executor.Message { return m[:2] },
		"a tool reply would be cut off from its call": func(m []executor.Message) []executor.Message {
			return append(m[:3:3], executor.Message{Role: "tool", Content: "result", ToolCallID: "x"})
		},
	} {
		in := edit(swapMessages())
		if got, c := coldCompactor(swapColdOnly).apply(in, swapConv, ""); c != nil || len(got) != len(in) {
			t.Errorf("%s: swapped %v, want the request as it came", name, c)
		}
	}
	// A partial or failed pack is never used.
	stagePack(t, msgs, idle.Verdict{Pass: true, Partial: true})
	if _, c := coldCompactor(swapColdOnly).apply(msgs, swapConv, ""); c != nil {
		t.Error("a partial pack was swapped in")
	}
	stagePack(t, msgs, idle.Verdict{Pass: false})
	if _, c := coldCompactor(swapColdOnly).apply(msgs, swapConv, ""); c != nil {
		t.Error("a pack that did not pass was swapped in")
	}
}

func TestServe_WarmCacheWaitsUnlessAutoPays(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	msgs := swapMessages()
	stagePack(t, msgs, passed)
	warm := func(c *compactor) *compactor {
		now := c.now().Add(-9 * time.Minute)
		c.now = func() time.Time { return now }
		return c
	}
	if _, c := warm(coldCompactor(swapColdOnly)).apply(msgs, swapConv, ""); c != nil {
		t.Error("cold_only swapped a warm cache")
	}
	if _, c := newCompactor(swapColdOnly).apply(msgs, swapConv, ""); c != nil {
		t.Error("a conversation first seen here has an unknown cache and must read as warm")
	}
	auto := warm(coldCompactor(swapAuto))
	if _, c := auto.apply(msgs, swapConv, ""); c != nil {
		t.Error("auto swapped a warm cache whose rates are unknown")
	}
	auto = warm(coldCompactor(swapAuto))
	auto.rates = func(string) idle.Rates { return idle.Rates{Write: 1.25, Read: 0.05, Known: true} }
	if _, c := auto.apply(msgs, swapConv, ""); c == nil {
		t.Error("auto did not swap a warm cache whose rewrite pays back")
	}
}

func TestServe_SwapOffByPolicy(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	msgs := swapMessages()
	stagePack(t, msgs, passed)
	got, c := coldCompactor(swapOff).apply(msgs, swapConv, "")
	if c != nil || len(got) != len(msgs) {
		t.Fatalf("swap = off still swapped: %v", c)
	}
	if err := validSwap("sometimes"); err == nil || validSwap("") != nil || validSwap(swapAuto) != nil {
		t.Fatal("validSwap must take off, cold_only, auto and empty, and refuse the rest")
	}
}

func TestServe_CompactedHeaderSet(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	msgs := swapMessages()
	stagePack(t, msgs, passed)
	r := serveRouter{defaultEnum: "STANDARD", compact: coldCompactor(swapColdOnly)}
	opts, ev, err := r.options(serve.Request{Messages: msgs, Conversation: swapConv})
	if err != nil {
		t.Fatal(err)
	}
	if ev == nil || ev.Compacted == nil || ev.Compacted.Pack != swapConv+"/1" || !opts.NoCache || len(opts.Messages) != 4 {
		t.Fatalf("evidence %+v, cache off %v, %d messages; want a compacted request that bypasses the cache", ev, opts.NoCache, len(opts.Messages))
	}
	if ev.TaskClass != "" || opts.Domain != "" {
		t.Fatal("a swap must not claim a task class")
	}
}

func TestServe_CompactorForgetsTheOldestConversationAtItsCap(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	c := newCompactor(swapColdOnly)
	start := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	for i := 0; i < maxConversations; i++ {
		c.now = func() time.Time { return start.Add(time.Duration(i) * time.Second) }
		c.since(fmt.Sprintf("conv-%d", i))
	}
	c.now = func() time.Time { return start.Add(time.Hour) }
	c.since("conv-new")
	if len(c.requests) != maxConversations {
		t.Fatalf("%d conversations kept, want the cap %d", len(c.requests), maxConversations)
	}
	if _, kept := c.requests["conv-0"]; kept {
		t.Fatal("the oldest conversation was kept")
	}
	if _, kept := c.requests["conv-1"]; !kept {
		t.Fatal("a newer conversation was dropped")
	}
}
