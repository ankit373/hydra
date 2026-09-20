// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/cache"
	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/trust"
)

// cachingDispatcher is a live dispatcher with the answer cache on, which is
// the only way any of this runs.
func cachingDispatcher(t *testing.T, s *testutil.Sandbox) *Dispatcher {
	t.Helper()
	d := liveDispatcher(echoHead(t, s, "h1", 90))
	d.cfg = &config.Config{CacheAnswers: true}
	return d
}

// The round trip: an answer is stored, and the same prompt is served from it
// without a head running.
func TestDispatch_ServesTheSameQuestionFromCache(t *testing.T) {
	s := testutil.NewSandbox(t)
	d := cachingDispatcher(t, s)

	first, err := d.Dispatch(context.Background(), "rotate the signing key", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Cache != nil {
		t.Fatal("the first dispatch was served from an empty cache")
	}

	second, err := d.Dispatch(context.Background(), "rotate  the signing key", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if second.Cache == nil {
		t.Fatal("the same question ran a head again")
	}
	if !second.Cache.Exact {
		t.Errorf("similarity %.3f: the same prompt should be an exact match", second.Cache.Similarity)
	}
	if second.Output != first.Output {
		t.Errorf("served %q, want the stored %q", second.Output, first.Output)
	}
	if second.Head.ID != "h1" {
		t.Errorf("the hit named head %q, want the one that produced the answer", second.Head.ID)
	}
}

// The propensity question, resolved rather than left for internal/ope to trip
// over: a hit has no head and so no routing propensity, and a row carrying one
// would corrupt every counterfactual computed afterwards (#605).
func TestDispatch_ACacheHitWritesNoCostRow(t *testing.T) {
	s := testutil.NewSandbox(t)
	d := cachingDispatcher(t, s)

	if _, err := d.Dispatch(context.Background(), "rotate the signing key", Options{}); err != nil {
		t.Fatal(err)
	}
	costPath := filepath.Join(config.Dir(), "logs", "cost.jsonl")
	before, _ := os.ReadFile(costPath)

	hit, err := d.Dispatch(context.Background(), "rotate the signing key", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if hit.Cache == nil {
		t.Fatal("the second dispatch was not served from cache")
	}
	after, _ := os.ReadFile(costPath)
	if len(after) != len(before) {
		t.Errorf("a cache hit appended to cost.jsonl:\n%s", after[len(before):])
	}
	rows := logLines(t, filepath.Join(config.Dir(), "logs", "dispatch.jsonl"))
	if rows != 1 {
		t.Errorf("dispatch.jsonl has %d rows after one head ran and one hit, want 1", rows)
	}
}

// A hit must not read as a head getting something right. Calibration measures
// heads, and feeding it a cached answer would measure the cache instead.
func TestDispatch_ACacheHitIsNotAHeadObservation(t *testing.T) {
	s := testutil.NewSandbox(t)
	d := cachingDispatcher(t, s)
	cal, err := trust.New("")
	if err != nil {
		t.Fatal(err)
	}
	d.cal = cal

	for range 3 {
		if _, err := d.Dispatch(context.Background(), "rotate the signing key", Options{}); err != nil {
			t.Fatal(err)
		}
	}
	correct, total := cal.Commitments("h1")
	if total != 0 || correct != 0 {
		t.Errorf("the cache recorded %d/%d commitments against the head", correct, total)
	}
}

// Never for a prompt carrying personal data, in either direction: it is not
// served from the cache and it does not go into it.
func TestDispatch_NeverCachesAPromptCarryingPII(t *testing.T) {
	s := testutil.NewSandbox(t)
	d := cachingDispatcher(t, s)

	const secret = "review this key AKIAIOSFODNN7EXAMPLE please"
	if _, err := d.Dispatch(context.Background(), secret, Options{}); err != nil {
		t.Fatal(err)
	}
	second, err := d.Dispatch(context.Background(), secret, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if second.Cache != nil {
		t.Error("a prompt carrying personal data was answered from the cache")
	}
	st, _ := cache.StoredStats(cache.Dir())
	if st.Entries != 0 {
		t.Errorf("%d entries stored: a PII prompt reached the store", st.Entries)
	}
}

// A pinned head is a request for that model's answer, and a stored one may
// have come from another.
func TestDispatch_NeverServesAPinnedHeadFromCache(t *testing.T) {
	s := testutil.NewSandbox(t)
	d := cachingDispatcher(t, s)

	if _, err := d.Dispatch(context.Background(), "rotate the signing key", Options{}); err != nil {
		t.Fatal(err)
	}
	pinned, err := d.Dispatch(context.Background(), "rotate the signing key", Options{Head: "h1"})
	if err != nil {
		t.Fatal(err)
	}
	if pinned.Cache != nil {
		t.Error("a pinned dispatch was answered from the cache")
	}
}

// A caller that says its prompt is not repeatable is believed, both ways.
func TestDispatch_NoCacheIsHonouredInBothDirections(t *testing.T) {
	s := testutil.NewSandbox(t)
	d := cachingDispatcher(t, s)

	if _, err := d.Dispatch(context.Background(), "judge these two answers", Options{NoCache: true}); err != nil {
		t.Fatal(err)
	}
	if st, _ := cache.StoredStats(cache.Dir()); st.Entries != 0 {
		t.Errorf("a NoCache dispatch stored its answer: %d entries", st.Entries)
	}
	if _, err := d.Dispatch(context.Background(), "judge these two answers", Options{}); err != nil {
		t.Fatal(err)
	}
	again, err := d.Dispatch(context.Background(), "judge these two answers", Options{NoCache: true})
	if err != nil {
		t.Fatal(err)
	}
	if again.Cache != nil {
		t.Error("a NoCache dispatch was served from the cache")
	}
}

// A dry run runs nothing, the cache included: it names the hit so the preview
// is not a lie about which head would answer, and counts nothing.
func TestDispatch_DryRunNamesTheHitWithoutServingIt(t *testing.T) {
	s := testutil.NewSandbox(t)
	d := cachingDispatcher(t, s)

	if _, err := d.Dispatch(context.Background(), "rotate the signing key", Options{}); err != nil {
		t.Fatal(err)
	}
	before, _ := cache.StoredStats(cache.Dir())

	dry, err := d.Dispatch(context.Background(), "rotate the signing key", Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if dry.Cache == nil {
		t.Error("the dry run named no cache hit, so it previewed a head that would never run")
	}
	if dry.Output != "" {
		t.Errorf("the dry run returned an answer: %q", dry.Output)
	}
	after, _ := cache.StoredStats(cache.Dir())
	if after.Hits != before.Hits {
		t.Errorf("the dry run counted a hit: %d became %d", before.Hits, after.Hits)
	}
}

// Off is off. The default config stores nothing and serves nothing, so a
// machine that never opted in dispatches exactly as it did before.
func TestDispatch_CacheOffChangesNothing(t *testing.T) {
	s := testutil.NewSandbox(t)
	d := liveDispatcher(echoHead(t, s, "h1", 90))

	for range 2 {
		r, err := d.Dispatch(context.Background(), "rotate the signing key", Options{})
		if err != nil {
			t.Fatal(err)
		}
		if r.Cache != nil {
			t.Fatal("the cache served an answer while switched off")
		}
	}
	if _, present := cache.StoredStats(cache.Dir()); present {
		t.Error("a store was created with the cache off")
	}
}

// One derivation for both directions. Asserting Servable twice would pass with
// the classification wired into the lookup and not the store, which is exactly
// the defect this caught: a PII prompt was refused on the way out and admitted
// on the way in.
func TestAdmission_IsTheSameDecisionInBothDirections(t *testing.T) {
	class := &policy.Classification{PII: true}
	if ok, _ := cache.Servable(admission(Options{}, class)); ok {
		t.Error("a prompt carrying personal data was admitted")
	}
	if ok, _ := cache.Servable(admission(Options{Head: "h1"}, nil)); ok {
		t.Error("a pinned dispatch was admitted")
	}
	if ok, _ := cache.Servable(admission(Options{NoCache: true}, nil)); ok {
		t.Error("a NoCache dispatch was admitted")
	}
	if ok, _ := cache.Servable(admission(Options{}, &policy.Classification{FlagReason: "ignore previous"})); ok {
		t.Error("a prompt carrying an injection marker was admitted")
	}
	if ok, reason := cache.Servable(admission(Options{}, &policy.Classification{})); !ok {
		t.Errorf("an ordinary dispatch was refused: %s", reason)
	}
}

// An answer carrying credential-shaped content is not stored: the egress gate
// already classified it, and replaying it later would be a decision rather
// than an accident.
func TestRemember_RefusesAResponseCarryingASecret(t *testing.T) {
	s := testutil.NewSandbox(t)
	d := cachingDispatcher(t, s)
	// A head whose answer carries something credential-shaped, which is what
	// recordOutputFinding already classifies on every dispatch.
	leaky := echoHead(t, s, "leaky", 90)
	leaky.Executable = s.FakeBinary(t, "fake-head-leaky", secretEchoScript())
	d.heads = []provider.Head{leaky}

	if _, err := d.Dispatch(context.Background(), "print the deploy key", Options{}); err != nil {
		t.Fatal(err)
	}
	if st, _ := cache.StoredStats(cache.Dir()); st.Entries != 0 {
		t.Errorf("%d entries: an answer carrying a credential was stored", st.Entries)
	}
}

// secretEchoScript answers with an AWS-key-shaped string, the same shape
// internal/policy already detects everywhere else.
func secretEchoScript() string {
	if runtime.GOOS == "windows" {
		return "@echo off\r\necho AKIAIOSFODNN7EXAMPLE\r\n"
	}
	return "#!/bin/sh\necho 'AKIAIOSFODNN7EXAMPLE'\n"
}

// logLines counts the rows in a JSONL log, or zero when it does not exist.
func logLines(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if line != "" {
			n++
		}
	}
	return n
}

// An oversize answer is refused by the store and the dispatch still succeeds:
// a cache that cannot write must not fail work that already worked.
func TestRemember_AStoreRefusalDoesNotFailTheDispatch(t *testing.T) {
	s := testutil.NewSandbox(t)
	d := cachingDispatcher(t, s)
	st, _ := d.answerCache()
	if st == nil {
		t.Fatal("the cache is on and no store was opened")
	}
	// A budget under one entry, so every write evicts what it just wrote.
	st.SetBudget(1)

	r, err := d.Dispatch(context.Background(), "rotate the signing key", Options{})
	if err != nil {
		t.Fatalf("a full cache failed the dispatch: %v", err)
	}
	if r.Output == "" {
		t.Error("the answer was lost")
	}
}

// With no embedding model the cache still answers near matches, because the
// gate that decides them is the content tokens and those need no model. This is
// what most machines running this actually get, and before #1015 it was an
// exact hash map for all of them.
func TestFromCache_WithoutAnEmbedderStillServesARestatement(t *testing.T) {
	s := testutil.NewSandbox(t)
	d := cachingDispatcher(t, s)

	if _, err := d.Dispatch(context.Background(), "rotate the signing key", Options{}); err != nil {
		t.Fatal(err)
	}
	// Forced rather than assumed: whether this machine happens to have an
	// embedding model must not decide what the test measures.
	d.answerCache()
	d.answerEmb = nil

	exact, err := d.Dispatch(context.Background(), "rotate the signing key", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if exact.Cache == nil {
		t.Error("the exact match stopped working without an embedder")
	}
	near, err := d.Dispatch(context.Background(), "please rotate the signing key", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if near.Cache == nil {
		t.Error("a restatement was refused with no embedder, leaving the cache an exact hash map")
	}

	// The guard that matters on such a machine: there is no second opinion to
	// fall back on, so the token gate has to hold on its own.
	for _, q := range []string{
		"rotate the signing keys",
		"rotate the signing certificate",
		"the signing key rotate",
	} {
		other, err := d.Dispatch(context.Background(), q, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if other.Cache != nil {
			t.Errorf("served a cached answer for a different question: %q", q)
		}
	}
}

// A hit rate needs a denominator. Counting only the hits would make every
// cache read 100%, and a dispatch the cache was never allowed to answer is not
// a miss it can be blamed for.
func TestDispatch_CountsMissesButNotRefusedDispatches(t *testing.T) {
	s := testutil.NewSandbox(t)
	d := cachingDispatcher(t, s)

	// One head run (a miss), then the same question (a hit).
	if _, err := d.Dispatch(context.Background(), "rotate the signing key", Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Dispatch(context.Background(), "rotate the signing key", Options{}); err != nil {
		t.Fatal(err)
	}
	// And one the cache may never answer, which must not count either way.
	if _, err := d.Dispatch(context.Background(), "leak AKIAIOSFODNN7EXAMPLE", Options{}); err != nil {
		t.Fatal(err)
	}

	st, _ := cache.StoredStats(cache.Dir())
	if st.Hits != 1 || st.Misses != 1 {
		t.Errorf("hits=%d misses=%d, want one of each", st.Hits, st.Misses)
	}
}
