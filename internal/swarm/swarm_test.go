// SPDX-License-Identifier: MIT

package swarm

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ankit373/hydra/internal/config"
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

// An explicitly pinned list must survive the default cap — otherwise pinning
// heads to escape the top-N CapScore crowding silently gets trimmed back to N.
func TestIDSelector_DefaultCapDoesNotTrimExplicitPins(t *testing.T) {
	var all []provider.Head
	var ids []string
	for i := 0; i < defaultMaxHeads+2; i++ {
		id := fmt.Sprintf("h%d", i)
		all = append(all, registryHead(id, id, 50))
		ids = append(ids, id)
	}

	got, err := (&IDSelector{}).Select(all, Options{HeadIDs: ids}) // MaxHeads unset
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(ids) {
		t.Errorf("pinned %d heads, got %d — the default cap trimmed an explicit list", len(ids), len(got))
	}

	// An explicit MaxHeads is a deliberate instruction and must still cap.
	got, err = (&IDSelector{}).Select(all, Options{HeadIDs: ids, MaxHeads: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("explicit MaxHeads=2 should cap to 2, got %d", len(got))
	}
}

// The default cap must still apply when heads were NOT explicitly pinned.
func TestCapScoreSelector_DefaultCapStillApplies(t *testing.T) {
	var all []provider.Head
	for i := 0; i < defaultMaxHeads+3; i++ {
		id := fmt.Sprintf("c%d", i)
		all = append(all, registryHead(id, id, 50))
	}
	got, err := (&CapScoreSelector{}).Select(all, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != defaultMaxHeads {
		t.Errorf("unpinned selection should cap at %d, got %d", defaultMaxHeads, len(got))
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

// estimateFanoutCost must report a cost even with no --swarm-max-cost limit.
// preflightCost short-circuits to 0 when no limit is set, which is right for a
// guard and wrong for the plan --dry-run prints (#167).
func TestEstimateFanoutCost_IndependentOfAnyLimit(t *testing.T) {
	heads := []provider.Head{registryHead("a", "A", 90), registryHead("b", "B", 80), registryHead("c", "C", 70)}
	pr := fakePricing{per: 0.01}

	if got := estimateFanoutCost(heads, "some prompt", pr); got != 0.03 {
		t.Errorf("estimateFanoutCost = %v, want 0.03", got)
	}
	// The guard reports nothing here — that difference is the whole point.
	if total, _ := preflightCost(heads, "some prompt", pr, 0); total != 0 {
		t.Errorf("preflightCost with no limit = %v, want 0", total)
	}
	if got := estimateFanoutCost(heads, "p", nil); got != 0 {
		t.Errorf("nil pricing = %v, want 0", got)
	}
}

// Plan must pick exactly the heads the corresponding run would, or --dry-run
// describes something other than what executing would do.
func TestPlan_SelectsSameHeadsAsRunAndExecutesNothing(t *testing.T) {
	all := []provider.Head{registryHead("h1", "H1", 90), registryHead("h2", "H2", 80), registryHead("h3", "H3", 70)}
	s := New(nil, all, fakePricing{per: 0.01})

	opts := Options{HeadIDs: []string{"h1", "h3"}}
	heads, est, err := s.Plan("some prompt", opts)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	// Same selector, same options — Plan must not diverge from the run path.
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	want, err := resolveSelector(opts, cfg).Select(all, opts)
	if err != nil {
		t.Fatalf("selector: %v", err)
	}
	if len(heads) != len(want) {
		t.Fatalf("Plan selected %d heads, the run path selects %d", len(heads), len(want))
	}
	for i := range want {
		if heads[i].ID != want[i].ID {
			t.Errorf("head %d: Plan chose %q, run path chooses %q", i, heads[i].ID, want[i].ID)
		}
	}
	if est != 0.02 {
		t.Errorf("est = %v, want 0.02 for 2 heads", est)
	}

	// s was built with a nil dispatcher: had Plan executed anything it would
	// have panicked rather than reached here.
}

func TestPlan_NoHeadsIsAnError(t *testing.T) {
	s := New(nil, nil, fakePricing{per: 0.01})
	if _, _, err := s.Plan("p", Options{}); err == nil {
		t.Error("Plan with no heads should error, not report an empty plan as fine")
	}
}
