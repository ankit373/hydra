// SPDX-License-Identifier: MIT

package swarm

import (
	"context"
	"errors"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
)

// registryHead returns a head that executor.Supports() accepts (Source=="registry"),
// so selector tests exercise real executability filtering without network.
func registryHead(id, name string, cap int) provider.Head {
	return provider.Head{ID: id, Name: name, Provider: "agy", Source: "registry", CapScore: cap}
}

// ── Attempt / SwarmResult ──────────────────────────────────────────────────────

func TestAttempt_SucceededAndTokens(t *testing.T) {
	a := Attempt{Status: StatusOK, InputTokens: 30, OutputTokens: 12}
	if !a.Succeeded() {
		t.Error("StatusOK should Succeed")
	}
	if a.TotalTokens() != 42 {
		t.Errorf("TotalTokens = %d, want 42", a.TotalTokens())
	}
	if (Attempt{Status: StatusFailed}).Succeeded() {
		t.Error("StatusFailed should not Succeed")
	}
}

func TestSwarmResult_SucceededCount(t *testing.T) {
	r := &SwarmResult{Attempts: []Attempt{
		{Status: StatusOK}, {Status: StatusFailed}, {Status: StatusOK}, {Status: StatusCanceled},
	}}
	if got := r.SucceededCount(); got != 2 {
		t.Errorf("SucceededCount = %d, want 2", got)
	}
}

func TestSuccessfulAttempts(t *testing.T) {
	got := successfulAttempts([]Attempt{{Status: StatusOK}, {Status: StatusFailed}, {Status: StatusOK}})
	if len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Errorf("successfulAttempts = %v, want [0 2]", got)
	}
}

// ── CapScoreJudge ──────────────────────────────────────────────────────────────

func TestCapScoreJudge_PicksHighest(t *testing.T) {
	attempts := []Attempt{
		{Head: registryHead("a", "A", 70), Status: StatusOK},
		{Head: registryHead("b", "B", 90), Status: StatusOK},
		{Head: registryHead("c", "C", 50), Status: StatusFailed},
	}
	v, err := (&CapScoreJudge{}).Judge(context.Background(), "", attempts)
	if err != nil {
		t.Fatal(err)
	}
	if v.WinnerIndex != 1 {
		t.Errorf("WinnerIndex = %d, want 1 (highest CapScore)", v.WinnerIndex)
	}
	if v.Scores[1] != 90 || v.Scores[0] != 70 {
		t.Errorf("Scores = %v, want [70 90 0]", v.Scores)
	}
	if v.Scores[2] != 0 {
		t.Errorf("failed attempt should score 0, got %d", v.Scores[2])
	}
}

func TestCapScoreJudge_NoSuccess(t *testing.T) {
	_, err := (&CapScoreJudge{}).Judge(context.Background(), "", []Attempt{{Status: StatusFailed}})
	if err == nil {
		t.Fatal("expected error when no attempt succeeded")
	}
}

// ── CompositeJudge ─────────────────────────────────────────────────────────────

type stubJudge struct {
	verdict *JudgeVerdict
	err     error
}

func (s stubJudge) Judge(context.Context, string, []Attempt) (*JudgeVerdict, error) {
	return s.verdict, s.err
}

func TestCompositeJudge_PrimarySucceeds(t *testing.T) {
	primary := stubJudge{verdict: &JudgeVerdict{WinnerIndex: 0}}
	fallback := stubJudge{err: errors.New("should not be called")}
	v, err := newCompositeJudge(primary, fallback).Judge(context.Background(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if v.Meta.UsedFallback {
		t.Error("UsedFallback should be false when primary succeeds")
	}
}

func TestCompositeJudge_FallsBack(t *testing.T) {
	primary := stubJudge{err: errors.New("primary boom")}
	fallback := stubJudge{verdict: &JudgeVerdict{WinnerIndex: 2}}
	v, err := newCompositeJudge(primary, fallback).Judge(context.Background(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Meta.UsedFallback {
		t.Error("UsedFallback should be true after fallback")
	}
	if v.Meta.FallbackReason != "primary boom" {
		t.Errorf("FallbackReason = %q, want %q", v.Meta.FallbackReason, "primary boom")
	}
}

func TestCompositeJudge_BothFail(t *testing.T) {
	primary := stubJudge{err: errors.New("p")}
	fallback := stubJudge{err: errors.New("f")}
	if _, err := newCompositeJudge(primary, fallback).Judge(context.Background(), "", nil); err == nil {
		t.Fatal("expected error when both judges fail")
	}
}

// ── Cost ───────────────────────────────────────────────────────────────────────

type fakePricing struct{ per float64 }

func (f fakePricing) EstimateCost(_ int, _, _ int) float64 { return f.per }

func TestPreflightCost(t *testing.T) {
	heads := []provider.Head{registryHead("a", "A", 90), registryHead("b", "B", 80), registryHead("c", "C", 70)}
	pr := fakePricing{per: 0.01}

	total, err := preflightCost(heads, "some prompt", pr, 0.05)
	if err != nil {
		t.Fatalf("under budget should not error: %v", err)
	}
	if total != 0.03 {
		t.Errorf("total = %v, want 0.03", total)
	}

	if _, err := preflightCost(heads, "some prompt", pr, 0.02); err == nil {
		t.Error("over budget should error")
	}

	if total, err := preflightCost(heads, "p", nil, 0.05); err != nil || total != 0 {
		t.Errorf("nil pricing: total=%v err=%v, want 0/nil", total, err)
	}
	if total, err := preflightCost(heads, "p", pr, 0); err != nil || total != 0 {
		t.Errorf("no limit: total=%v err=%v, want 0/nil", total, err)
	}
}

func TestEnrichCosts(t *testing.T) {
	attempts := []Attempt{
		{Head: registryHead("a", "A", 90), Status: StatusOK, InputTokens: 100, OutputTokens: 50},
		{Head: registryHead("b", "B", 80), Status: StatusFailed},
	}
	enrichCosts(attempts, fakePricing{per: 0.02})
	if attempts[0].EstCostUSD != 0.02 {
		t.Errorf("OK attempt cost = %v, want 0.02", attempts[0].EstCostUSD)
	}
	if attempts[1].EstCostUSD != 0 {
		t.Errorf("failed attempt cost = %v, want 0 (untouched)", attempts[1].EstCostUSD)
	}
}

func TestRound6(t *testing.T) {
	if got := round6(0.123456789); got != 0.123457 {
		t.Errorf("round6 = %v, want 0.123457", got)
	}
}

// ── Selectors ──────────────────────────────────────────────────────────────────

func TestIDSelector(t *testing.T) {
	all := []provider.Head{registryHead("h1", "H1", 90), registryHead("h2", "H2", 80), registryHead("h3", "H3", 70)}

	got, err := (&IDSelector{}).Select(all, Options{HeadIDs: []string{"h1", "h3"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("selected %d heads, want 2", len(got))
	}

	if _, err := (&IDSelector{}).Select(all, Options{HeadIDs: []string{"h1", "missing"}}); err == nil {
		t.Error("missing head ID should error")
	}
}

func TestCapScoreSelector_SortsDescending(t *testing.T) {
	all := []provider.Head{registryHead("lo", "Lo", 50), registryHead("hi", "Hi", 90), registryHead("mid", "Mid", 70)}
	got, err := (&CapScoreSelector{}).Select(all, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != "hi" || got[1].ID != "mid" || got[2].ID != "lo" {
		t.Errorf("order = %v, want hi>mid>lo", []string{got[0].ID, got[1].ID, got[2].ID})
	}
}

func TestCapScoreSelector_NoExecutableHeads(t *testing.T) {
	// A cli-sourced head with no known template is not executable.
	all := []provider.Head{{ID: "x", Source: "cli", Provider: "unknown-vendor"}}
	if _, err := (&CapScoreSelector{}).Select(all, Options{}); err == nil {
		t.Error("expected error when no heads are executable")
	}
}

func TestCapScoreSelector_MaxHeadsCap(t *testing.T) {
	var all []provider.Head
	for i, id := range []string{"a", "b", "c", "d"} {
		all = append(all, registryHead(id, id, 90-i))
	}
	got, err := (&CapScoreSelector{}).Select(all, Options{MaxHeads: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("MaxHeads=2 returned %d heads", len(got))
	}
}
