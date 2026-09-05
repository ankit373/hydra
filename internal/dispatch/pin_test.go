// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

// brokenHead exits non-zero, so the executor fails and the chain advances. It
// is the shape that produced #676: a strong head that cannot answer, with a
// weaker one behind it ready to answer instead.
func brokenHead(t *testing.T, s *testutil.Sandbox, id string, capScore int) provider.Head {
	t.Helper()
	body := "#!/bin/sh\necho 'boom' >&2\nexit 1\n"
	if runtime.GOOS == "windows" {
		body = "@echo off\r\necho boom 1>&2\r\nexit /b 1\r\n"
	}
	return provider.Head{
		ID: id, Name: id, Provider: "openai", Source: "cli",
		CapScore: capScore, AuthReady: true,
		Executable: s.FakeBinary(t, "fake-head-"+id, body),
	}
}

// The defect: the strong head fails, a weaker one answers, and the result says
// only who answered. A caller that can see just Head cannot tell this apart
// from having routed to the weak head deliberately.
func TestDispatch_ResultNamesEveryFailedAttempt(t *testing.T) {
	s := testutil.NewSandbox(t)

	res, err := liveDispatcher(
		brokenHead(t, s, "strong", 95),
		echoHead(t, s, "weak", 50),
	).Dispatch(context.Background(), "do the thing", Options{RunID: "run-att", TaskID: "task-att"})
	if err != nil {
		t.Fatalf("dispatch failed outright: %v", err)
	}
	if res.Head.ID != "weak" {
		t.Fatalf("answered by %q, want the fallback head", res.Head.ID)
	}
	if len(res.Attempts) != 1 {
		t.Fatalf("Attempts = %+v, want the one head that failed", res.Attempts)
	}
	a := res.Attempts[0]
	if a.Head != "strong" {
		t.Errorf("Attempts[0].Head = %q, want strong", a.Head)
	}
	if a.Reason == "" {
		t.Error("Attempts[0].Reason is empty; the caller still cannot say why it fell back")
	}
	if res.Retries != len(res.Attempts) {
		t.Errorf("Retries = %d but %d attempts recorded; they must agree", res.Retries, len(res.Attempts))
	}
}

// A run that needs no fallback must not report one, or every ordinary reply
// grows a "we tried something else" line that is not true.
func TestDispatch_NoAttemptsWhenTheFirstHeadAnswers(t *testing.T) {
	s := testutil.NewSandbox(t)

	res, err := liveDispatcher(echoHead(t, s, "fine", 95)).
		Dispatch(context.Background(), "do the thing", Options{RunID: "run-ok", TaskID: "task-ok"})
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if len(res.Attempts) != 0 {
		t.Errorf("Attempts = %+v on a clean run, want none", res.Attempts)
	}
}

// A ledger denial is a reason too. It never reaches an executor, so the error
// text alone would not explain the fallback.
func TestDispatch_DeniedHeadIsRecordedAsAnAttempt(t *testing.T) {
	s := testutil.NewSandbox(t)
	writeLedgerPolicy(t, ledger.Policy{
		Rules:   []ledger.Rule{{Tool: "blocked", Decision: ledger.Deny}},
		Default: ledger.Allow,
	})

	res, err := liveDispatcher(
		echoHead(t, s, "blocked", 95),
		echoHead(t, s, "allowed", 50),
	).Dispatch(context.Background(), "do the thing", Options{RunID: "run-den", TaskID: "task-den"})
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if res.Head.ID != "allowed" {
		t.Fatalf("answered by %q, want the allowed head", res.Head.ID)
	}
	if len(res.Attempts) != 1 || res.Attempts[0].Head != "blocked" {
		t.Fatalf("Attempts = %+v, want the denied head recorded", res.Attempts)
	}
	if !strings.Contains(res.Attempts[0].Reason, "polic") {
		t.Errorf("Reason = %q, want it to name the policy", res.Attempts[0].Reason)
	}
}

// Picking a model is a request for that model. Pinning must never answer from
// a different one, which is the whole of #676: the picker offered a choice the
// router was free to ignore.
func TestDispatch_PinnedHeadNeverFallsThrough(t *testing.T) {
	s := testutil.NewSandbox(t)

	_, err := liveDispatcher(
		brokenHead(t, s, "chosen", 95),
		echoHead(t, s, "other", 50),
	).Dispatch(context.Background(), "do the thing", Options{
		Head: "chosen", RunID: "run-pin", TaskID: "task-pin",
	})
	if err == nil {
		t.Fatal("a pinned head that cannot answer must fail, not substitute another model")
	}
}

func TestDispatch_PinnedHeadRunsWhenItCan(t *testing.T) {
	s := testutil.NewSandbox(t)

	res, err := liveDispatcher(
		echoHead(t, s, "strong", 95),
		echoHead(t, s, "chosen", 50),
	).Dispatch(context.Background(), "do the thing", Options{
		Head: "chosen", RunID: "run-pin2", TaskID: "task-pin2",
	})
	if err != nil {
		t.Fatalf("pinned dispatch failed: %v", err)
	}
	if res.Head.ID != "chosen" {
		t.Errorf("answered by %q, want the pinned head even though a stronger one exists", res.Head.ID)
	}
}

// An unknown id must say so by name. "no dispatchable heads" alone reads as
// "this machine has nothing", which sends the user looking in the wrong place.
func TestDispatch_PinnedHeadUnknownNamesIt(t *testing.T) {
	s := testutil.NewSandbox(t)

	_, err := liveDispatcher(echoHead(t, s, "real", 95)).
		Dispatch(context.Background(), "do the thing", Options{Head: "ghost"})
	if !errors.Is(err, ErrNoHeads) {
		t.Fatalf("err = %v, want ErrNoHeads", err)
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("err = %q, want it to name the head that was asked for", err)
	}
}

// The cost row's pool came only from head metadata, and exactly one provider
// (agy) ever set it. Every other provider's rows landed with pool="" and their
// pool card read 0 calls forever, on 90% of a real machine's rows (#681).
func TestHeadPool_ResolvesFromTheRegistryWhenMetadataIsAbsent(t *testing.T) {
	// The shape the port provider actually discovers: no metadata at all.
	h := provider.Head{ID: "ollama/Qwen2.5-Coder:7b", Name: "Qwen2.5-Coder:7b (Ollama)"}
	if got := headPool(h); got != "local_ollama" {
		t.Errorf("headPool(%s) = %q, want local_ollama from the registry", h.ID, got)
	}
}

// Metadata still wins where a provider does attach it, so agy keeps working
// exactly as before.
func TestHeadPool_ProviderMetadataWins(t *testing.T) {
	h := provider.Head{ID: "ollama/Qwen2.5-Coder:7b", Meta: map[string]string{"token_pool": "explicit"}}
	if got := headPool(h); got != "explicit" {
		t.Errorf("headPool = %q, want the provider's own metadata to win", got)
	}
}

// An unrecognised head records no pool rather than guessing one: a wrong pool
// files spend against someone else's quota, which is worse than none.
func TestHeadPool_UnknownHeadRecordsNoPool(t *testing.T) {
	if got := headPool(provider.Head{ID: "nothing/we-know"}); got != "" {
		t.Errorf("headPool = %q, want empty for an unknown head", got)
	}
}
