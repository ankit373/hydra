// SPDX-License-Identifier: MIT

package cache

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The system prompt is part of what was asked. Proved with one that carries
// the answer: under "the secret code is BRAVO" the cache served ALPHA's
// answer, because neither the key nor the content gate had ever seen it.
func TestLookup_ADifferentSystemPromptIsADifferentQuestion(t *testing.T) {
	s := open(t)
	if err := s.Put(Entry{
		Prompt: "what is the secret code", System: "the secret code is ALPHA",
		Response: "ALPHA", Head: "h1",
	}); err != nil {
		t.Fatal(err)
	}

	bravo := Query{Prompt: "what is the secret code", System: "the secret code is BRAVO"}
	if out := s.Lookup(bravo, nil, DefaultThreshold); out.Found {
		t.Errorf("a different system prompt was served %q", out.Hit.Response)
	}
	// Same words in another order is still another system prompt, so the
	// content gate must not reach it either.
	reordered := Query{Prompt: "what is the secret code", System: "ALPHA is the secret code"}
	if out := s.Lookup(reordered, nil, DefaultThreshold); out.Found {
		t.Errorf("a reordered system prompt was served %q", out.Hit.Response)
	}
	alpha := Query{Prompt: "what is the secret code", System: "the secret code is ALPHA"}
	if out := s.Lookup(alpha, nil, DefaultThreshold); !out.Found {
		t.Error("the system prompt that produced the answer was not served it")
	}
}

// An operator reading answers.jsonl has to be able to tell which instructions
// produced an entry, which the store did not record at all.
func TestPut_RecordsTheSystemPromptItWasAskedUnder(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Entry{
		Prompt: "what is the secret code", System: "the secret code is ALPHA",
		Response: "ALPHA", Head: "h1",
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "answers.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "the secret code is ALPHA") {
		t.Errorf("the stored answer does not say what framed it:\n%s", raw)
	}
}

// A fragment stored is a fragment replayed for ever, and the replay reports
// that it finished normally. Refused outright rather than flagged, since the
// flag would then have to be carried by every surface that renders an answer.
func TestPut_RefusesATruncatedAnswer(t *testing.T) {
	s := open(t)
	err := s.Put(Entry{Prompt: "list every routing enum", Response: "SIMPLE, MOD", Head: "h1", Truncated: true})
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("storing a capped answer returned %v, want ErrTruncated", err)
	}
	if got := s.Stat().Entries; got != 0 {
		t.Errorf("%d entries after a refused write, want 0", got)
	}
	if out := s.Lookup(Query{Prompt: "list every routing enum"}, nil, DefaultThreshold); out.Found {
		t.Errorf("the capped answer was served anyway: %q", out.Hit.Response)
	}
}

// A cap constrains the answer, so a run with a small one must not poison the
// entry for a run with a large one.
func TestLookup_ADifferentCapIsADifferentQuestion(t *testing.T) {
	s := open(t)
	if err := s.Put(Entry{
		Prompt: "list every routing enum", MaxTokens: 16, Response: "SIMPLE", Head: "h1",
	}); err != nil {
		t.Fatal(err)
	}
	if out := s.Lookup(Query{Prompt: "list every routing enum", MaxTokens: 4096}, nil, DefaultThreshold); out.Found {
		t.Errorf("a 16-token answer was served to a 4096-token request: %q", out.Hit.Response)
	}
	if out := s.Lookup(Query{Prompt: "list every routing enum", MaxTokens: 16}, nil, DefaultThreshold); !out.Found {
		t.Error("the cap that produced the answer was not served it")
	}
}

// Serving down is defensible, serving up defeats the one escalation mechanism
// in the product: `hyctl dispatch --tier 2` after a weak answer has to run a
// head, not hand back the weak answer the user is escalating away from.
func TestLookup_AWeakerTiersAnswerIsNotServedToAStrongerRequest(t *testing.T) {
	s := open(t)
	if err := s.Put(Entry{Prompt: "is this migration safe", Tier: 10, Response: "probably", Head: "local"}); err != nil {
		t.Fatal(err)
	}
	out := s.Lookup(Query{Prompt: "is this migration safe", Tier: 2}, nil, DefaultThreshold)
	if out.Found {
		t.Errorf("a tier-10 answer was served to a tier-2 escalation: %q", out.Hit.Response)
	}
	if !out.Refused {
		t.Error("holding the answer and declining to serve it is a refusal, not a miss")
	}
	if out := s.Lookup(Query{Prompt: "is this migration safe", Tier: 10}, nil, DefaultThreshold); !out.Found {
		t.Error("the tier that produced the answer was not served it")
	}
}

// The other direction: a stronger head's answer is at least as good as the one
// a weaker request would have got, so it is served.
func TestLookup_AStrongerTiersAnswerIsStillServedDown(t *testing.T) {
	s := open(t)
	if err := s.Put(Entry{Prompt: "is this migration safe", Tier: 2, Response: "yes", Head: "h1"}); err != nil {
		t.Fatal(err)
	}
	if out := s.Lookup(Query{Prompt: "is this migration safe", Tier: 10}, nil, DefaultThreshold); !out.Found {
		t.Error("a tier-2 answer was refused to a tier-10 request, which costs a head run for nothing")
	}
}

// The near-match path has to respect the tier too, or the escalation is
// defeated by one reworded prompt on a machine with no embedder.
func TestLookup_TheNearPathRefusesAWeakerTier(t *testing.T) {
	s := open(t)
	if err := s.Put(Entry{Prompt: "rotate the signing key", Tier: 10, Response: "x", Head: "local"}); err != nil {
		t.Fatal(err)
	}
	if out := s.Lookup(Query{Prompt: "please rotate the signing key", Tier: 2}, nil, DefaultThreshold); out.Found {
		t.Errorf("a restatement walked past the tier gate: %q", out.Hit.Response)
	}
}
