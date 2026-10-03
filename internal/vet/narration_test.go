// SPDX-License-Identifier: MIT

package vet

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// narratingThenAnsweringRouter narrates the first time it is asked about a
// file, the way an agentic CLI opens a single-shot review prompt, and answers
// for real on the second ask, which is what the retry exists to recover.
type narratingThenAnsweringRouter struct {
	mu    sync.Mutex
	calls map[string]int
}

func (r *narratingThenAnsweringRouter) callCount(resource string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[resource]
}

func (r *narratingThenAnsweringRouter) Review(_ context.Context, prompt, _, resource string) (Answer, error) {
	r.mu.Lock()
	if r.calls == nil {
		r.calls = map[string]int{}
	}
	r.calls[resource]++
	n := r.calls[resource]
	r.mu.Unlock()

	if n == 1 {
		return Answer{
			Output: "Let me examine the file and the `Request` type to understand the full context.",
			Head:   "agy", CostUSD: 0.01, InputTokens: 100, OutputTokens: 20,
		}, nil
	}
	if !strings.Contains(prompt, narrationContract) {
		return Answer{}, fmt.Errorf("retry prompt did not carry the stricter instruction")
	}
	return Answer{
		Output: `[{"line":3,"severity":"blocking","title":"real finding"}]`,
		Head:   "agy", CostUSD: 0.01, InputTokens: 100, OutputTokens: 15,
	}, nil
}

func TestReviewFile_RetriesOnceWhenTheReplyNarrates(t *testing.T) {
	dir := gitRepo(t)
	write(t, dir, "a.go", "package p\n\nfunc A() {}\n")
	spec := &Spec{
		Mode: "workspace", Repository: dir,
		Reviewable: []File{{Path: "a.go"}},
		Groups:     []Group{{Pattern: "**/*.go", Files: []string{"a.go"}, Rule: "be careful"}},
	}
	router := &narratingThenAnsweringRouter{}

	res, err := Run(context.Background(), router, spec, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 1 {
		t.Fatalf("got %d files, want 1", len(res.Files))
	}
	out := res.Files[0]
	if out.Unparsed {
		t.Fatalf("reported unreadable after a retry that answered: %+v", out)
	}
	if res.Reviewed() != 1 {
		t.Fatalf("reviewed = %d, want 1: a narrated-then-answered file must count as reviewed", res.Reviewed())
	}
	if res.BlockingCount() != 1 {
		t.Fatalf("blocking = %d, want 1: the retry's finding was lost", res.BlockingCount())
	}
	if got := router.callCount("a.go"); got != 2 {
		t.Fatalf("called the router %d times, want exactly 2: one ask plus one retry", got)
	}
	// Both dispatches really ran, so both must be charged; the first attempt's
	// spend must not vanish once the retry succeeds.
	if out.CostUSD != 0.02 {
		t.Errorf("cost = %v, want 0.02 (both attempts)", out.CostUSD)
	}
}

// mergeRetry must carry forward what the first attempt actually spent: it is
// a real dispatch whether or not the retry parses, and FileOutcome only ever
// sees the merged total, never the first attempt's Answer directly.
func TestMergeRetry_CarriesForwardBothAttemptsSpend(t *testing.T) {
	first := Answer{Output: "narration", Head: "agy", CostUSD: 0.01, InputTokens: 100, OutputTokens: 20}
	retry := Answer{Output: "[]", Head: "agy", CostUSD: 0.015, InputTokens: 120, OutputTokens: 5}

	got := mergeRetry(first, retry)
	if got.CostUSD != 0.025 {
		t.Errorf("CostUSD = %v, want 0.025", got.CostUSD)
	}
	if got.InputTokens != 220 || got.OutputTokens != 25 {
		t.Errorf("tokens = %d/%d, want 220/25", got.InputTokens, got.OutputTokens)
	}
	if got.Output != "[]" {
		t.Errorf("Output = %q, want the retry's own content", got.Output)
	}
}

// alwaysNarratesRouter is a head that cannot do this at all: every reply, the
// retry included, is a narration. The file must be reported unreadable, never
// silently folded into a pass, and never dispatched a third time.
type alwaysNarratesRouter struct {
	mu    sync.Mutex
	calls int
}

func (r *alwaysNarratesRouter) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *alwaysNarratesRouter) Review(_ context.Context, _, _, _ string) (Answer, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	return Answer{Output: "I'll start by reading the file to see what changed.", Head: "agy", CostUSD: 0.01}, nil
}

func TestReviewFile_StaysUnreadableAfterOneRetryAndSpendsNoMore(t *testing.T) {
	dir := gitRepo(t)
	write(t, dir, "a.go", "package p\n")
	spec := &Spec{
		Mode: "workspace", Repository: dir,
		Reviewable: []File{{Path: "a.go"}},
		Groups:     []Group{{Files: []string{"a.go"}, Rule: "r"}},
	}
	router := &alwaysNarratesRouter{}

	res, err := Run(context.Background(), router, spec, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	out := res.Files[0]
	if !out.Unparsed {
		t.Fatal("a reply that narrated twice was not reported as unreadable")
	}
	if out.Raw == "" {
		t.Error("the raw reply was dropped, so --json cannot show what came back")
	}
	if res.Reviewed() != 0 {
		t.Errorf("reviewed = %d: a file unreadable after its retry was counted as reviewed", res.Reviewed())
	}
	if got := router.callCount(); got != 2 {
		t.Fatalf("called the router %d times, want exactly 2: never spend a third time", got)
	}
	if out.CostUSD != 0.02 {
		t.Errorf("cost = %v, want 0.02: both failed attempts still spent and must be reported", out.CostUSD)
	}
}
