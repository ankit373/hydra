// SPDX-License-Identifier: MIT

package swarm

import (
	"context"
	"errors"
	"fmt"
	"github.com/ankit373/hydra/internal/config"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/provider"
)

// The judge decides which of N paid answers the user actually gets. Its parser
// reads free-form model output, so every malformed shape it accepts is a wrong
// answer presented as the best one.

func okAttempt(id string, capScore int, output string) Attempt {
	return Attempt{
		Head:   provider.Head{ID: id, Name: id, CapScore: capScore},
		Status: StatusOK,
		Output: output,
	}
}

func failedAttempt(id string) Attempt {
	return Attempt{
		Head:   provider.Head{ID: id, Name: id, CapScore: 90},
		Status: StatusFailed,
		Err:    errors.New("boom"),
	}
}

func TestParseJudgeResponse_AcceptsNoisyOutput(t *testing.T) {
	successIdx := []int{0, 1}

	tests := []struct {
		name       string
		raw        string
		wantWinner int
	}{{
		name:       "bare json",
		raw:        `{"winner":1,"scores":[70,85],"reason":"clearer"}`,
		wantWinner: 1,
	}, {
		// Models wrap JSON in a fence even when told not to.
		name:       "fenced json",
		raw:        "```json\n{\"winner\":0,\"scores\":[90,60],\"reason\":\"correct\"}\n```",
		wantWinner: 0,
	}, {
		name:       "prose either side",
		raw:        "Here is my evaluation:\n{\"winner\":1,\"scores\":[10,20],\"reason\":\"x\"}\nHope that helps!",
		wantWinner: 1,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := parseJudgeResponse(tt.raw, 2, successIdx)
			if err != nil {
				t.Fatal(err)
			}
			if v.WinnerIndex != tt.wantWinner {
				t.Errorf("WinnerIndex = %d, want %d", v.WinnerIndex, tt.wantWinner)
			}
			if v.Reason == "" {
				t.Error("Reason is empty; it is what the user is shown to justify the pick")
			}
		})
	}
}

// Every malformed verdict must be an error. A judge that "succeeds" on garbage
// hands the user an arbitrary answer and calls it the best one.
func TestParseJudgeResponse_RejectsMalformedVerdicts(t *testing.T) {
	tests := []struct {
		name          string
		raw           string
		totalAttempts int
		successIdx    []int
	}{{
		name: "not json at all", raw: "I prefer the second one.",
		totalAttempts: 2, successIdx: []int{0, 1},
	}, {
		name: "truncated json", raw: `{"winner":0,"scores":[1,2]`,
		totalAttempts: 2, successIdx: []int{0, 1},
	}, {
		// A score per response is what makes the verdict auditable; a short
		// array means the judge did not evaluate them all.
		name: "too few scores", raw: `{"winner":0,"scores":[90],"reason":"x"}`,
		totalAttempts: 2, successIdx: []int{0, 1},
	}, {
		name: "too many scores", raw: `{"winner":0,"scores":[90,80,70],"reason":"x"}`,
		totalAttempts: 2, successIdx: []int{0, 1},
	}, {
		// The judge picked a response that does not exist. Indexing on it would
		// panic in Run, which dereferences attempts[verdict.WinnerIndex].
		name: "winner out of range", raw: `{"winner":7,"scores":[90,80],"reason":"x"}`,
		totalAttempts: 2, successIdx: []int{0, 1},
	}, {
		// It picked an attempt that failed, there is no output to return.
		name: "winner is a failed attempt", raw: `{"winner":1,"scores":[90],"reason":"x"}`,
		totalAttempts: 2, successIdx: []int{0},
	}, {
		name: "negative winner", raw: `{"winner":-1,"scores":[90,80],"reason":"x"}`,
		totalAttempts: 2, successIdx: []int{0, 1},
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := parseJudgeResponse(tt.raw, tt.totalAttempts, tt.successIdx)
			if err == nil {
				t.Fatalf("parseJudgeResponse accepted %s: %+v", tt.name, v)
			}
		})
	}
}

// Scores are reported against the full attempt list, so a failed attempt scores
// zero rather than shifting every later score up by one.
func TestParseJudgeResponse_MapsScoresBackOntoFailedAttempts(t *testing.T) {
	// Attempt 1 failed; only 0 and 2 were judged.
	v, err := parseJudgeResponse(`{"winner":2,"scores":[60,95],"reason":"x"}`, 3, []int{0, 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Scores) != 3 {
		t.Fatalf("Scores = %v, want one per attempt", v.Scores)
	}
	if v.Scores[0] != 60 || v.Scores[1] != 0 || v.Scores[2] != 95 {
		t.Errorf("Scores = %v, want [60 0 95], the failed attempt must score 0 "+
			"rather than shifting the others", v.Scores)
	}
}

// buildJudgePrompt must carry every successful candidate and its index, or the
// judge is scoring something other than what it is shown.
func TestBuildJudgePrompt_CarriesEveryCandidate(t *testing.T) {
	attempts := []Attempt{
		okAttempt("alpha", 90, "answer from alpha"),
		failedAttempt("beta"),
		okAttempt("gamma", 80, "answer from gamma"),
	}
	got := buildJudgePrompt("the original question", attempts, []int{0, 2})

	for _, want := range []string{
		"the original question",
		"answer from alpha", "answer from gamma",
		"alpha", "gamma",
		"Response 0", "Response 2",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("judge prompt is missing %q", want)
		}
	}
	// The failed attempt has no output to score and must not appear as a
	// candidate, the judge would otherwise be asked to rank an empty answer.
	if strings.Contains(got, "Response 1 ") {
		t.Errorf("the failed attempt was offered as a candidate:\n%s", got)
	}
	if !strings.Contains(got, `{"winner"`) {
		t.Error("the prompt does not state the response format it will be parsed against")
	}
}

// One successful attempt needs no LLM call: there is nothing to compare.
// Spending a tier-1 dispatch to pick the only candidate is pure waste.
func TestLLMJudge_SingleSuccessSkipsTheDispatch(t *testing.T) {
	j := newLLMJudge(nil, "1", 0) // a nil dispatcher would panic if used

	attempts := []Attempt{failedAttempt("a"), okAttempt("b", 88, "the only answer")}
	v, err := j.Judge(context.Background(), "q", attempts)
	if err != nil {
		t.Fatal(err)
	}
	if v.WinnerIndex != 1 {
		t.Errorf("WinnerIndex = %d, want the only successful attempt", v.WinnerIndex)
	}
	if v.Scores[1] != 88 {
		t.Errorf("Scores = %v, want the head's CapScore for the sole candidate", v.Scores)
	}
	if v.Reason == "" {
		t.Error("no reason given")
	}
}

func TestLLMJudge_NoSuccessfulAttemptsIsAnError(t *testing.T) {
	j := newLLMJudge(nil, "1", 0)
	if v, err := j.Judge(context.Background(), "q", []Attempt{failedAttempt("a")}); err == nil {
		t.Errorf("Judge returned %+v with nothing to judge", v)
	}
}

func TestNewLLMJudge_DefaultsTheTimeout(t *testing.T) {
	if got := newLLMJudge(nil, "1", 0); got.timeout != defaultJudgeTimeout {
		t.Errorf("timeout = %v with none set, want the default %v, zero would "+
			"cancel the judge immediately", got.timeout, defaultJudgeTimeout)
	}
	if got := newLLMJudge(nil, "1", 5*time.Second); got.timeout != 5*time.Second {
		t.Errorf("timeout = %v, want the caller's 5s", got.timeout)
	}
}

// The CapScore judge is the deterministic fallback used when the LLM judge
// fails. It must never pick a failed attempt.
func TestCapScoreJudge_PicksTheStrongestSuccess(t *testing.T) {
	attempts := []Attempt{
		okAttempt("weak", 60, "a"),
		failedAttempt("strongest-but-failed"), // CapScore 90
		okAttempt("strong", 85, "b"),
	}

	v, err := (&CapScoreJudge{}).Judge(context.Background(), "q", attempts)
	if err != nil {
		t.Fatal(err)
	}
	if v.WinnerIndex != 2 {
		t.Errorf("WinnerIndex = %d, want the strongest *successful* attempt", v.WinnerIndex)
	}

	if _, err := (&CapScoreJudge{}).Judge(context.Background(), "q", []Attempt{failedAttempt("a")}); err == nil {
		t.Error("CapScoreJudge returned a winner with no successful attempts")
	}
}

// ── winner selection ──────────────────────────────────────────────────────────

func TestRaceWinner_FallsBackToTheFirstSuccess(t *testing.T) {
	ranked := []Attempt{okAttempt("a", 90, "x"), okAttempt("b", 80, "y")}
	ranked[1].Rank = 1
	if got := raceWinner(ranked); got == nil || got.Head.ID != "b" {
		t.Errorf("raceWinner = %+v, want the rank-1 attempt", got)
	}

	// Nothing ranked: the first success stands in rather than nil, which would
	// report a successful race with no answer.
	unranked := []Attempt{failedAttempt("a"), okAttempt("b", 80, "y")}
	if got := raceWinner(unranked); got == nil || got.Head.ID != "b" {
		t.Errorf("raceWinner with nothing ranked = %+v, want the first success", got)
	}

	if got := raceWinner([]Attempt{failedAttempt("a")}); got != nil {
		t.Errorf("raceWinner = %+v with no successes, want nil", got)
	}
}

func TestCapScoreWinner_MarksTheWinnerAndIgnoresFailures(t *testing.T) {
	attempts := []Attempt{
		failedAttempt("failed-but-strong"), // 90
		okAttempt("mid", 70, "x"),
		okAttempt("best", 85, "y"),
	}
	got := capScoreWinner(attempts)
	if got == nil || got.Head.ID != "best" {
		t.Fatalf("capScoreWinner = %+v, want the strongest success", got)
	}
	if attempts[2].Rank != 1 {
		t.Error("the winner was not marked rank 1 in the attempt list the caller keeps")
	}
	if capScoreWinner([]Attempt{failedAttempt("a")}) != nil {
		t.Error("capScoreWinner returned a winner with no successes")
	}
}

// `--swarm-mode all` ranks everything so the user can compare. Failed attempts
// must stay unranked rather than being given a place in the ordering.
func TestRankByCapScore(t *testing.T) {
	attempts := []Attempt{
		okAttempt("mid", 70, "x"),
		failedAttempt("failed"),
		okAttempt("best", 95, "y"),
		okAttempt("worst", 50, "z"),
	}
	rankByCapScore(attempts)

	if attempts[2].Rank != 1 || attempts[0].Rank != 2 || attempts[3].Rank != 3 {
		t.Errorf("ranks = %d/%d/%d/%d, want best=1 mid=2 worst=3",
			attempts[0].Rank, attempts[1].Rank, attempts[2].Rank, attempts[3].Rank)
	}
	if attempts[1].Rank != 0 {
		t.Errorf("the failed attempt was ranked %d; it has no output to rank",
			attempts[1].Rank)
	}
}

func TestFirstSuccessful(t *testing.T) {
	if got := firstSuccessful([]Attempt{failedAttempt("a"), okAttempt("b", 1, "x")}); got == nil || got.Head.ID != "b" {
		t.Errorf("firstSuccessful = %+v", got)
	}
	if got := firstSuccessful(nil); got != nil {
		t.Errorf("firstSuccessful(nil) = %+v, want nil", got)
	}
}

// truncate bounds the prompt preview written to cost.jsonl. It counts runes,
// not bytes, cutting a multi-byte character in half produces invalid UTF-8 in
// a JSON log.
func TestTruncate_CountsRunesNotBytes(t *testing.T) {
	if got := truncate("short", 80); got != "short" {
		t.Errorf("truncate = %q", got)
	}

	long := strings.Repeat("é", 100) // two bytes each
	got := truncate(long, 80)
	if n := len([]rune(got)); n != 81 {
		t.Errorf("truncate produced %d runes, want 80 plus the ellipsis", n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Error("truncated text is not marked as cut")
	}
	for _, r := range got {
		if r == '�' {
			t.Fatalf("truncate split a multi-byte rune: %q", got)
		}
	}
}

// classifyError decides how a head's failure is reported and, for auth, whether
// the user is told to log in. Reading them all as generic failures hides the
// one the user can act on.
func TestClassifyError_EveryStatus(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want HeadStatus
	}{
		{"nil is ok", nil, StatusOK},
		{"auth required", &executor.AuthRequiredError{ModelFlag: "m", Pool: "p"}, StatusAuthRequired},
		{"wrapped auth required",
			errors.New("outer: " + (&executor.AuthRequiredError{}).Error()), StatusFailed},
		{"deadline", context.DeadlineExceeded, StatusTimeout},
		{"canceled", context.Canceled, StatusCanceled},
		{"anything else", errors.New("boom"), StatusFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyError(tt.err); got != tt.want {
				t.Errorf("classifyError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}

	// Wrapping must not hide the type, errors.As unwraps, and that is what
	// keeps "run: claude login" reaching the user through a fallback chain.
	wrapped := fmt.Errorf("dispatch failed: %w", &executor.AuthRequiredError{ModelFlag: "m"})
	if got := classifyError(wrapped); got != StatusAuthRequired {
		t.Errorf("a wrapped AuthRequiredError classified as %v, want AuthRequired", got)
	}
	deadline := fmt.Errorf("head timed out: %w", context.DeadlineExceeded)
	if got := classifyError(deadline); got != StatusTimeout {
		t.Errorf("a wrapped deadline classified as %v, want Timeout", got)
	}
}

// ── selectors ─────────────────────────────────────────────────────────────────

// TierSelector resolves a named config tier. When that tier has no live heads it
// falls back to capability ranking rather than returning nothing, a swarm that
// silently engages zero heads is indistinguishable from one that ran.
func TestTierSelector_FallsBackWhenTheTierIsEmpty(t *testing.T) {
	heads := []provider.Head{
		registryHeadFor("strong", 95),
		registryHeadFor("mid", 70),
	}
	cfg := &config.Config{Tiers: []config.Tier{
		{Name: "expert", Heads: []string{"strong"}},
		{Name: "ghost", Heads: []string{"a-head-that-is-not-installed"}},
	}}
	sel := &TierSelector{cfg: cfg}

	// A tier with a live head selects exactly it.
	got, err := sel.Select(heads, Options{TierHint: "expert"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "strong" {
		t.Errorf("Select(expert) = %+v, want just the configured head", got)
	}

	// A tier whose heads are all absent falls back to capability ranking rather
	// than selecting nothing.
	got, err = sel.Select(heads, Options{TierHint: "ghost"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Error("a tier with no live heads selected nothing; the swarm would " +
			"report a run that engaged no head")
	}

	// A tier name that is not in the config at all does the same.
	got, err = sel.Select(heads, Options{TierHint: "never-configured"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Error("an unknown tier name selected nothing")
	}

	// MinCapScore still filters after the fallback, so a floor is not lost by
	// taking the fallback path.
	got, err = sel.Select(heads, Options{TierHint: "ghost", MinCapScore: 90})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range got {
		if h.CapScore < 90 {
			t.Errorf("%s scored %d, below the requested floor of 90", h.ID, h.CapScore)
		}
	}
}

// IDSelector resolves explicit head ids. A typo'd id must be reported, not
// silently dropped, the user asked for a specific head.
func TestIDSelector_ReportsAnUnknownID(t *testing.T) {
	heads := []provider.Head{registryHeadFor("known", 90)}
	sel := &IDSelector{}

	got, err := sel.Select(heads, Options{HeadIDs: []string{"known"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "known" {
		t.Errorf("Select = %+v", got)
	}

	if _, err := sel.Select(heads, Options{HeadIDs: []string{"known", "typo"}}); err == nil {
		t.Error("an unknown head id was silently dropped; the user asked for it " +
			"by name")
	}
}

func registryHeadFor(id string, capScore int) provider.Head {
	return provider.Head{
		ID: id, Name: id, Provider: "agy", Source: "registry", Executable: "/usr/bin/agy",
		CapScore: capScore, AuthReady: true,
	}
}

// ── equivalence parsing ───────────────────────────────────────────────────────

// parseYesNo is covered in sprt_test.go; this pins the prompt it parses.

// buildEquivalencePrompt must carry both answers and the original question, or
// the judge is comparing something other than what it was asked about.
func TestBuildEquivalencePrompt_CarriesBothAnswers(t *testing.T) {
	got := buildEquivalencePrompt("is the migration safe?",
		"yes, with a backfill", "safe if you backfill first")
	for _, want := range []string{
		"is the migration safe?", "yes, with a backfill", "safe if you backfill first",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the prompt omits %q:\n%s", want, got)
		}
	}
}
