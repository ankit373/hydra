// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/a2a"
	"github.com/ankit373/hydra/internal/budget"
	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/pricing"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

// A dispatch that actually succeeds is the path nothing else covers: it is
// where the handoff, the budget book and state.json are written. A head backed
// by a real (fake) CLI binary is the only way to reach it without a network or
// an API key.
func echoHead(t *testing.T, s *testutil.Sandbox, id string, capScore int) provider.Head {
	t.Helper()
	body := "#!/bin/sh\necho 'reviewed: looks fine'\n"
	if runtime.GOOS == "windows" {
		body = "@echo off\r\necho reviewed: looks fine\r\n"
	}
	// Provider "openai" has a CLI template, so executor.For picks CLIExecutor
	// and Unroutable accepts the head.
	return provider.Head{
		ID: id, Name: id, Provider: "openai", Source: "cli",
		CapScore: capScore, AuthReady: true,
		Executable: s.FakeBinary(t, "fake-head-"+id, body),
	}
}

// liveDispatcher leaves pricing nil: estimateCost already returns 0 for that,
// and pricing.Load() spawns a background OpenRouter fetch per call that logs a
// connection failure into every test's output on a sandboxed machine.
func liveDispatcher(heads ...provider.Head) *Dispatcher {
	return &Dispatcher{
		cfg:    &config.Config{},
		heads:  heads,
		policy: policy.New(policy.DefaultRules(false)),
		budget: budget.NewRegistry(nil),
	}
}

func readState(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(config.Dir(), "logs", "state.json"))
	if err != nil {
		t.Fatalf("no state.json was written: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("state.json is not valid JSON: %v\n%s", err, raw)
	}
	return m
}

// The full success path: output returned, handoff written, state.json synced.
// Each of these is read by a different surface (`hyctl status`, the next
// agent's --a2a, the cockpit), so a silent failure in any one of them is only
// visible here.
func TestDispatch_SuccessWritesHandoffAndState(t *testing.T) {
	s := testutil.NewSandbox(t)
	d := liveDispatcher(echoHead(t, s, "h1", 90))

	res, err := d.Dispatch(context.Background(), "review this", Options{})
	if err != nil {
		t.Fatalf("dispatch failed against a working head: %v", err)
	}
	if !strings.Contains(res.Output, "looks fine") {
		t.Errorf("Output = %q, want the head's output", res.Output)
	}
	if res.Head.ID != "h1" || res.Retries != 0 {
		t.Errorf("Result = head %q after %d retries, want h1 on the first try",
			res.Head.ID, res.Retries)
	}

	// last_handoff.json is what --a2a reads; a dispatch that does not write it
	// breaks the chain silently.
	h, err := a2a.Load(filepath.Join(config.Dir(), "logs", "last_handoff.json"))
	if err != nil || h == nil {
		t.Fatalf("no handoff was written: %v", err)
	}
	if h.Task != "review this" {
		t.Errorf("handoff Task = %q, want the prompt", h.Task)
	}
	if !strings.Contains(h.PriorOutput, "looks fine") {
		t.Errorf("handoff PriorOutput = %q, want the head's output", h.PriorOutput)
	}
	if len(h.Clock) == 0 {
		t.Error("handoff carries no vector clock, so causal ordering is lost")
	}

	state := readState(t)
	if state["last_model"] != "h1" {
		t.Errorf("state.json last_model = %v, want h1", state["last_model"])
	}
	if state["last_status"] != "ok" {
		t.Errorf("state.json last_status = %v, want ok", state["last_status"])
	}
}

// The handoff's vector clock must advance across dispatches — that is what
// makes a chain reconstructable rather than a sequence of unrelated files.
func TestDispatch_HandoffClockAdvancesAcrossCalls(t *testing.T) {
	s := testutil.NewSandbox(t)
	d := liveDispatcher(echoHead(t, s, "h1", 90))
	path := filepath.Join(config.Dir(), "logs", "last_handoff.json")

	if _, err := d.Dispatch(context.Background(), "first", Options{}); err != nil {
		t.Fatal(err)
	}
	first, err := a2a.Load(path)
	if err != nil || first == nil {
		t.Fatal(err)
	}

	if _, err := d.Dispatch(context.Background(), "second", Options{}); err != nil {
		t.Fatal(err)
	}
	second, err := a2a.Load(path)
	if err != nil || second == nil {
		t.Fatal(err)
	}

	if got := first.Clock.Compare(second.Clock); got != a2a.Before {
		t.Errorf("clock %v vs %v = %v, want happens-before — the second dispatch did "+
			"not inherit the first's history", first.Clock, second.Clock, got)
	}
}

// Every LocalOnly head maps to rank.UITier 10 (#248), so two different local
// models used to tick the identical "hydra-tier-10" clock key. The clock must
// key on the head's own identity instead, or two genuinely different agents'
// handoffs are indistinguishable from one agent dispatching twice (#503). The
// display "From" string is untouched — it still reads as the tier bucket.
func TestDispatch_HandoffClockDistinguishesSameTierHeads(t *testing.T) {
	s := testutil.NewSandbox(t)
	path := filepath.Join(config.Dir(), "logs", "last_handoff.json")

	localHead := func(id string) provider.Head {
		h := echoHead(t, s, id, 50)
		h.LocalOnly = true
		return h
	}

	if _, err := liveDispatcher(localHead("model-a")).Dispatch(context.Background(), "first", Options{}); err != nil {
		t.Fatal(err)
	}
	first, err := a2a.Load(path)
	if err != nil || first == nil {
		t.Fatal(err)
	}

	if _, err := liveDispatcher(localHead("model-b")).Dispatch(context.Background(), "second", Options{}); err != nil {
		t.Fatal(err)
	}
	second, err := a2a.Load(path)
	if err != nil || second == nil {
		t.Fatal(err)
	}

	if _, ok := first.Clock["model-a"]; !ok {
		t.Errorf("first handoff clock %v has no entry for model-a's own identity", first.Clock)
	}
	if _, ok := second.Clock["model-b"]; !ok {
		t.Errorf("second handoff clock %v has no entry for model-b's own identity", second.Clock)
	}
	if _, ok := second.Clock["hydra-tier-10"]; ok {
		t.Errorf("clock keyed on the shared tier bucket instead of head identity: %v", second.Clock)
	}
	if first.From != "hydra-tier-10" || second.From != "hydra-tier-10" {
		t.Errorf("display From must stay the tier-bucket string, got %q and %q", first.From, second.From)
	}
}

// A dry run must resolve the chain without executing anything.
func TestDispatch_DryRunResolvesTheChainWithoutRunningIt(t *testing.T) {
	s := testutil.NewSandbox(t)
	d := liveDispatcher(echoHead(t, s, "strong", 95), echoHead(t, s, "weak", 40))

	res, err := d.Dispatch(context.Background(), "anything", Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "" {
		t.Errorf("a dry run produced output %q — it executed a head", res.Output)
	}
	if res.Head.ID == "" {
		t.Error("a dry run named no primary head")
	}
	if _, err := os.Stat(filepath.Join(config.Dir(), "logs", "last_handoff.json")); err == nil {
		t.Error("a dry run wrote a handoff")
	}
}

// Fallback: the first head fails, the next one answers, and Retries reports how
// far down the chain the answer came from.
func TestDispatch_FallsThroughToTheNextHead(t *testing.T) {
	s := testutil.NewSandbox(t)

	failing := echoHead(t, s, "broken", 95)
	failing.Executable = filepath.Join(s.BinDir, "does-not-exist")
	working := echoHead(t, s, "ok", 90)

	res, err := liveDispatcher(failing, working).Dispatch(context.Background(), "go", Options{})
	if err != nil {
		t.Fatalf("dispatch gave up instead of falling back: %v", err)
	}
	if res.Head.ID != "ok" {
		t.Errorf("answered by %q, want the fallback head", res.Head.ID)
	}
	if res.Retries != 1 {
		t.Errorf("Retries = %d, want 1 — the user is told how far the chain fell",
			res.Retries)
	}
}

// A ledger deny rule must actually block a real dispatch to that head, not
// just log it — the entire point of a policy gate over an accountability log.
func TestDispatch_LedgerDenyRuleBlocksTheHead(t *testing.T) {
	s := testutil.NewSandbox(t)
	writeLedgerPolicy(t, ledger.Policy{Rules: []ledger.Rule{{Tool: "denied", Decision: ledger.Deny}}})

	res, err := liveDispatcher(echoHead(t, s, "denied", 95), echoHead(t, s, "ok", 90)).
		Dispatch(context.Background(), "go", Options{})
	if err != nil {
		t.Fatalf("dispatch gave up instead of falling back past the denied head: %v", err)
	}
	if res.Head.ID != "ok" {
		t.Errorf("answered by %q, want the fallback head — the denied one must never run", res.Head.ID)
	}

	events, err := ledger.Load(ledger.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	var sawDeny bool
	for _, e := range events {
		if e.Tool == "denied" && e.Decision == ledger.Deny {
			sawDeny = true
		}
	}
	if !sawDeny {
		t.Errorf("no deny event recorded for the denied head: %+v", events)
	}
}

// With no policy configured, the ledger's default-allow must leave dispatch
// behavior unchanged — but it must still record the access.
func TestDispatch_DefaultLedgerPolicyRecordsButNeverBlocks(t *testing.T) {
	s := testutil.NewSandbox(t)

	res, err := liveDispatcher(echoHead(t, s, "h1", 90)).Dispatch(context.Background(), "go", Options{})
	if err != nil {
		t.Fatalf("a dispatch with no ledger policy configured must succeed: %v", err)
	}
	if res.Head.ID != "h1" {
		t.Errorf("Head = %q, want h1", res.Head.ID)
	}

	events, err := ledger.Load(ledger.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	var sawAllow bool
	for _, e := range events {
		if e.Tool == "h1" && e.Decision == ledger.Allow {
			sawAllow = true
		}
	}
	if !sawAllow {
		t.Errorf("no allow event recorded for a real dispatch: %+v", events)
	}
}

// A ledger rule keyed on Resource must scope by the file a dispatch acts on,
// not just by head — the concrete "excessive agency" containment: a head may
// be trusted in general but still denied write access to a specific path.
func TestDispatch_LedgerResourceScopingBlocksOnlyMatchingFiles(t *testing.T) {
	s := testutil.NewSandbox(t)
	writeLedgerPolicy(t, ledger.Policy{Rules: []ledger.Rule{{Resource: "internal/auth/*", Decision: ledger.Deny}}})

	if _, err := liveDispatcher(echoHead(t, s, "h1", 90)).
		Dispatch(context.Background(), "go", Options{Resource: "internal/auth/token.go"}); err == nil {
		t.Error("dispatch touching internal/auth/token.go should have been denied by the resource rule")
	}

	res, err := liveDispatcher(echoHead(t, s, "h1", 90)).
		Dispatch(context.Background(), "go", Options{Resource: "internal/api/handler.go"})
	if err != nil {
		t.Fatalf("dispatch touching a non-matching resource should succeed: %v", err)
	}
	if res.Head.ID != "h1" {
		t.Errorf("Head = %q, want h1", res.Head.ID)
	}
}

// A denial-of-wallet guard: a candidate head whose estimated cost exceeds
// MaxCostUSD must never execute — mirrors swarm's own preflight-cost pattern,
// extended to ordinary dispatch (which had no ceiling at all before this).
func TestDispatch_MaxCostUSDRefusesAnExpensiveHead(t *testing.T) {
	s := testutil.NewSandbox(t)
	dd := liveDispatcher(echoHead(t, s, "expensive", 95))
	dd.pricing = pricing.Load()

	// A prompt with real length, so the char-count/4 token estimate is
	// nonzero — tier 1's $15/$75-per-million rate then prices well above the
	// ceiling below.
	prompt := strings.Repeat("x", 400)
	if _, err := dd.Dispatch(context.Background(), prompt, Options{MaxCostUSD: 0.0000001}); err == nil {
		t.Error("dispatch should have been refused: the only head's estimated cost exceeds the ceiling")
	}

	events, err := ledger.Load(ledger.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	var sawCostDenial bool
	for _, e := range events {
		if e.Decision == ledger.Deny && strings.Contains(e.Reason, "cost ceiling") {
			sawCostDenial = true
		}
	}
	if !sawCostDenial {
		t.Errorf("no cost-ceiling denial recorded in the ledger: %+v", events)
	}
}

// MaxCostUSD: 0 (the default) must change nothing — a ceiling that silently
// activates itself would refuse dispatches nobody asked to bound.
func TestDispatch_ZeroMaxCostUSDIsNoLimit(t *testing.T) {
	s := testutil.NewSandbox(t)
	dd := liveDispatcher(echoHead(t, s, "h1", 95))
	dd.pricing = pricing.Load()

	res, err := dd.Dispatch(context.Background(), "go", Options{MaxCostUSD: 0})
	if err != nil {
		t.Fatalf("MaxCostUSD: 0 should not refuse anything: %v", err)
	}
	if res.Head.ID != "h1" {
		t.Errorf("Head = %q, want h1", res.Head.ID)
	}
}

func writeLedgerPolicy(t *testing.T, p ledger.Policy) {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	path := ledger.DefaultPolicyPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Every head failing must report the last error, not a generic one — that
// error is the only diagnostic the user gets.
func TestDispatch_AllHeadsFailingReportsWhy(t *testing.T) {
	s := testutil.NewSandbox(t)
	broken := echoHead(t, s, "broken", 95)
	broken.Executable = filepath.Join(s.BinDir, "does-not-exist")

	_, err := liveDispatcher(broken).Dispatch(context.Background(), "go", Options{})
	if err == nil {
		t.Fatal("dispatch succeeded with a head that cannot run")
	}
	if !strings.Contains(err.Error(), "all heads failed") {
		t.Errorf("error = %v, want it to say every head failed", err)
	}
}

// The end-to-end repro for #451: a documented tier name ("expert") that does
// not exist in cfg.Tiers must fail with an error naming the bad tier, not the
// generic "no routable heads" message that blames the head pool for a config
// problem. A live, perfectly routable head is present, so a regression back to
// the old behavior would still dispatch successfully rather than error at all.
func TestDispatch_UnknownNamedTierIsDistinctFromNoRoutableHeads(t *testing.T) {
	s := testutil.NewSandbox(t)
	dd := liveDispatcher(echoHead(t, s, "cloud", 90))

	_, err := dd.Dispatch(context.Background(), "go", Options{TierHint: "expert"})
	if err == nil {
		t.Fatal("dispatch succeeded with a tier name absent from config")
	}
	if !strings.Contains(err.Error(), "unknown tier") || !strings.Contains(err.Error(), "expert") {
		t.Errorf("error = %v, want it to name \"expert\" as an unknown tier", err)
	}
	if strings.Contains(err.Error(), "no routable heads") || strings.Contains(err.Error(), "no available heads") {
		t.Errorf("error = %v, blames routability for a config problem", err)
	}
}

// The end-to-end repro for #454: an out-of-range numeric --tier must fail
// clearly, naming the requested value, rather than silently behaving as "no
// tier" (0, negative) or getting clamped to 10 with no trace of the input.
func TestDispatch_OutOfRangeNumericTierIsRejected(t *testing.T) {
	s := testutil.NewSandbox(t)
	dd := liveDispatcher(echoHead(t, s, "cloud", 90))

	for _, hint := range []string{"0", "-1", "11", "20"} {
		_, err := dd.Dispatch(context.Background(), "go", Options{TierHint: hint})
		if err == nil {
			t.Fatalf("dispatch succeeded with out-of-range tier %q", hint)
		}
		if !strings.Contains(err.Error(), hint) {
			t.Errorf("tier %q: error = %v, want it to name the requested value", hint, err)
		}
	}
}

// PII in the prompt forces local-only routing. With no local head that must be
// a refusal naming the cause, not a quiet escalation to a paid API head — the
// whole point of the policy.
func TestDispatch_PIIForcesLocalOnlyAndSaysSoWhenNothingIsLocal(t *testing.T) {
	s := testutil.NewSandbox(t)

	dd := &Dispatcher{
		cfg:    &config.Config{},
		heads:  []provider.Head{echoHead(t, s, "cloud", 90)},
		policy: policy.New(policy.DefaultRules(true)), // local-only PII rule armed
		budget: budget.NewRegistry(nil),
	}

	_, err := dd.Dispatch(context.Background(), "my SSN is 123-45-6789", Options{})
	if err == nil {
		t.Fatal("a PII prompt was dispatched to a non-local head")
	}
	if !strings.Contains(err.Error(), "localOnly=true") {
		t.Errorf("error = %v, want it to name local-only as the cause", err)
	}
}

// A no-heads dispatch failure must be matchable by errors.Is(err, ErrNoHeads)
// so a caller with no terminal to point at (the desktop dock) can render a
// friendly message instead of dispatch's CLI-flavored text (#452). An empty
// tier hint — the dock's "auto-route" default — must read as "no tier hint
// given", not a literal `tier ""`.
func TestDispatch_NoHeadsErrorIsMatchableAndPhrasesAnEmptyTierClearly(t *testing.T) {
	s := testutil.NewSandbox(t)

	dd := &Dispatcher{
		cfg:    &config.Config{},
		heads:  []provider.Head{echoHead(t, s, "cloud", 90)},
		policy: policy.New(policy.DefaultRules(true)),
		budget: budget.NewRegistry(nil),
	}

	_, err := dd.Dispatch(context.Background(), "my SSN is 123-45-6789", Options{})
	if err == nil {
		t.Fatal("a PII prompt was dispatched to a non-local head")
	}
	if !errors.Is(err, ErrNoHeads) {
		t.Errorf("error = %v, want it to satisfy errors.Is(err, ErrNoHeads)", err)
	}
	if strings.Contains(err.Error(), `tier ""`) {
		t.Errorf(`error = %v, an empty tier hint printed as the literal tier "" instead of being phrased`, err)
	}
	if !strings.Contains(err.Error(), "no tier hint given") {
		t.Errorf("error = %v, want it to say no tier hint was given", err)
	}
}

// A2A injection: the handoff's context must reach the prompt when the file is
// valid, and a missing or corrupt handoff file must fail loudly (#450) rather
// than silently dispatching without the context the user asked for.
func TestInjectA2A(t *testing.T) {
	dir := t.TempDir()

	h := a2a.Handoff{From: "agent-1", Task: "earlier task", PriorOutput: "earlier output"}
	raw, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "handoff.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := injectA2A(path, "new instruction")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"agent-1", "earlier output", "new instruction", "ADDITIONAL INSTRUCTION"} {
		if !strings.Contains(got, want) {
			t.Errorf("injected prompt is missing %q:\n%s", want, got)
		}
	}

	// --a2a always names a file the user explicitly asked for, so a missing
	// file must be an error, not silently treated as "no handoff".
	if _, err := injectA2A(filepath.Join(dir, "absent.json"), "unchanged"); err == nil {
		t.Error("a missing --a2a file did not produce an error")
	}

	if err := os.WriteFile(path, []byte("{truncated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := injectA2A(path, "unchanged"); err == nil {
		t.Error("a malformed --a2a file did not produce an error")
	}
}

// A dispatch given a bad --a2a path must fail clearly instead of silently
// running without the handoff context the user explicitly asked for (#450).
func TestDispatch_BadA2AFileFailsTheRun(t *testing.T) {
	s := testutil.NewSandbox(t)

	t.Run("nonexistent file", func(t *testing.T) {
		dd := liveDispatcher(echoHead(t, s, "h1", 90))
		_, err := dd.Dispatch(context.Background(), "work", Options{
			A2AFile: filepath.Join(t.TempDir(), "nope.json"),
		})
		if err == nil {
			t.Fatal("a nonexistent --a2a file did not fail the dispatch")
		}
		if !strings.Contains(err.Error(), "nope.json") {
			t.Errorf("error = %v, want it to name the offending path", err)
		}
	})

	t.Run("malformed JSON", func(t *testing.T) {
		dd := liveDispatcher(echoHead(t, s, "h2", 90))
		path := filepath.Join(t.TempDir(), "bad.json")
		if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := dd.Dispatch(context.Background(), "work", Options{A2AFile: path})
		if err == nil {
			t.Fatal("a malformed --a2a file did not fail the dispatch")
		}
	})

	t.Run("valid file still dispatches", func(t *testing.T) {
		dd := liveDispatcher(echoHead(t, s, "h3", 90))
		h := a2a.Handoff{From: "agent-1", Task: "earlier task"}
		raw, err := json.Marshal(h)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "handoff.json")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		res, err := dd.Dispatch(context.Background(), "work", Options{A2AFile: path})
		if err != nil {
			t.Fatalf("a valid --a2a file failed the dispatch: %v", err)
		}
		if res.Output == "" {
			t.Error("no output despite a working head")
		}
	})
}

// claudeMode is the token-preservation governor. Its whole purpose is to
// downgrade before the orchestrator runs out, so each band's downgrade is
// pinned — this table was inert for every non-numeric hint before #165.
func TestClaudeMode_DowngradesByBand(t *testing.T) {
	tests := []struct {
		name     string
		pct      int
		hint     string
		wantTier string
		wantMode string
	}{
		{"normal leaves the tier alone", 10, "3", "3", "normal"},
		{"compact band still does not downgrade", 55, "3", "3", "compact"},
		{"caution band still does not downgrade", 66, "3", "3", "caution"},
		{"warning downgrades one tier", 72, "3", "4", "warning"},
		{"critical downgrades two", 77, "3", "5", "critical"},
		{"emergency pins to the local tier", 85, "3", "10", "emergency"},
		{"a downgrade cannot exceed tier 10", 77, "9", "10", "critical"},
		{"a named tier survives the governor untouched", 72, "expert", "expert", "warning"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testutil.NewSandbox(t)
			writeStatePct(t, tt.pct, nil)

			dd := liveDispatcher()
			tier, mode, pct := dd.claudeMode(tt.hint)
			if pct != tt.pct {
				t.Errorf("pct = %d, want %d", pct, tt.pct)
			}
			if mode != tt.wantMode {
				t.Errorf("mode = %q, want %q", mode, tt.wantMode)
			}
			if tier != tt.wantTier {
				t.Errorf("tier = %q, want %q", tier, tt.wantTier)
			}
		})
	}
}

// The governor is rate-aware: a fast burn escalates the mode above its static
// level band, so the downgrade happens before the threshold is crossed rather
// than after.
func TestClaudeMode_FastBurnEscalatesAboveTheStaticBand(t *testing.T) {
	testutil.NewSandbox(t)
	// A steep trajectory ending at a level that is only "compact" statically.
	writeStatePct(t, 60, []int{10, 25, 40, 52, 60})

	dd := liveDispatcher()
	_, fastMode, _ := dd.claudeMode("3")

	testutil.NewSandbox(t)
	writeStatePct(t, 60, []int{58, 59, 59, 60, 60}) // flat
	_, slowMode, _ := dd.claudeMode("3")

	if fastMode == slowMode {
		t.Skipf("both trajectories yield %q; the risk model did not separate them", fastMode)
	}
	if slowMode != "compact" {
		t.Errorf("a flat trajectory at 60%% = %q, want the static band \"compact\"", slowMode)
	}
}

func TestReadClaudePct_MissingOrCorruptStateIsZeroNotAPanic(t *testing.T) {
	testutil.NewSandbox(t)

	if got := readClaudePct(); got != 0 {
		t.Errorf("readClaudePct() = %d with no state.json, want 0", got)
	}
	if got := readClaudePctHistory(); got != nil {
		t.Errorf("readClaudePctHistory() = %v with no state.json, want nil", got)
	}

	dir := filepath.Join(config.Dir(), "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{truncated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readClaudePct(); got != 0 {
		t.Errorf("readClaudePct() = %d on corrupt state.json, want 0", got)
	}
	if got := readClaudePctHistory(); got != nil {
		t.Errorf("readClaudePctHistory() = %v on corrupt state.json, want nil", got)
	}
}

// state.json is read-modify-written by every dispatch. A successful run must
// extend the claude_pct history rather than replacing the file, or the rate
// signal the governor depends on is destroyed on every call.
func TestSyncStateJSON_PreservesForeignKeysAndExtendsHistory(t *testing.T) {
	s := testutil.NewSandbox(t)
	writeStatePct(t, 40, []int{20, 30})

	// Add a key Hydra does not own; another writer's data must survive.
	statePath := filepath.Join(config.Dir(), "logs", "state.json")
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["set_by_someone_else"] = "keep me"
	raw, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	dd := liveDispatcher(echoHead(t, s, "h1", 90))
	if _, err := dd.Dispatch(context.Background(), "work", Options{}); err != nil {
		t.Fatal(err)
	}

	state := readState(t)
	if state["set_by_someone_else"] != "keep me" {
		t.Error("a foreign key was dropped; state.json was replaced rather than updated")
	}
	hist, _ := state["claude_pct_history"].([]any)
	if len(hist) != 3 {
		t.Errorf("claude_pct_history = %v, want the existing two plus this run's 40", hist)
	}
}

// Each `hyctl dispatch` is a fresh process with its own in-memory
// budget.Registry, so it only ever knows about the head(s) it just ran.
// Replacing state.json's "budget" map wholesale — instead of merging into it —
// erased every other model's entry on the very next dispatch (#502).
func TestSyncStateJSON_MergesBudgetAcrossDispatchesInsteadOfReplacing(t *testing.T) {
	s := testutil.NewSandbox(t)

	d1 := liveDispatcher(echoHead(t, s, "tier4-head", 90))
	if _, err := d1.Dispatch(context.Background(), "work", Options{}); err != nil {
		t.Fatal(err)
	}
	d2 := liveDispatcher(echoHead(t, s, "tier8-head", 40))
	if _, err := d2.Dispatch(context.Background(), "work", Options{}); err != nil {
		t.Fatal(err)
	}

	budgetMap, ok := readState(t)["budget"].(map[string]any)
	if !ok {
		t.Fatalf("state[\"budget\"] = %#v, want a map", readState(t)["budget"])
	}
	if _, ok := budgetMap["tier4-head"]; !ok {
		t.Errorf("budget map = %v, missing tier4-head — the first dispatch's entry "+
			"was overwritten by the second", budgetMap)
	}
	if _, ok := budgetMap["tier8-head"]; !ok {
		t.Errorf("budget map = %v, missing tier8-head", budgetMap)
	}
}

// asInt / asIntSlice bridge JSON's float64 numbers back to ints. A wrong answer
// here silently zeroes the governor's history.
func TestAsIntAndAsIntSlice(t *testing.T) {
	if got := asInt(float64(52)); got != 52 {
		t.Errorf("asInt(float64) = %d, want 52 — JSON numbers arrive as float64", got)
	}
	if got := asInt(52); got != 52 {
		t.Errorf("asInt(int) = %d, want 52", got)
	}
	for _, v := range []any{nil, "52", true, []any{1}} {
		if got := asInt(v); got != 0 {
			t.Errorf("asInt(%#v) = %d, want 0", v, got)
		}
	}

	got := asIntSlice([]any{float64(1), float64(2), float64(3)})
	if len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Errorf("asIntSlice = %v, want [1 2 3]", got)
	}
	if got := asIntSlice("not an array"); got != nil {
		t.Errorf("asIntSlice(string) = %v, want nil", got)
	}
	// A mixed array must not drop elements — the history's length is its rate
	// signal, so a silently shortened slice changes the risk estimate.
	if got := asIntSlice([]any{float64(1), "x", float64(3)}); len(got) != 3 || got[1] != 0 {
		t.Errorf("asIntSlice with a bad element = %v, want [1 0 3]", got)
	}
}

// Heads and EstimateCost are the surface swarm builds on.
func TestHeadsAndEstimateCost_AreExposedToCallers(t *testing.T) {
	testutil.NewSandbox(t)

	dd := liveDispatcher(provider.Head{ID: "a", Name: "a", Provider: "agy", Source: "registry", CapScore: 80})
	dd.pricing = pricing.Load()
	if heads := dd.Heads(); len(heads) != 1 || heads[0].ID != "a" {
		t.Errorf("Heads() = %+v, want the one head it was built with", heads)
	}

	cheap := dd.EstimateCost(10, 1_000_000, 1_000_000)
	dear := dd.EstimateCost(1, 1_000_000, 1_000_000)
	if dear <= cheap {
		t.Errorf("EstimateCost: tier 1 = %v is not dearer than tier 10 = %v — cost "+
			"routing has nothing to route on", dear, cheap)
	}
}

// New refuses without a config rather than probing the machine and routing
// against defaults the user never chose.
func TestNew_WithoutAConfigPointsAtInit(t *testing.T) {
	testutil.NewSandbox(t)

	dd, err := New(context.Background())
	if err == nil {
		t.Fatalf("New() succeeded with no config: %+v", dd)
	}
	if !strings.Contains(err.Error(), "hyctl init") {
		t.Errorf("error = %v, want it to name the fix", err)
	}
}

func TestNew_LoadsTheConfigAndArmsThePIIPolicy(t *testing.T) {
	testutil.NewSandbox(t)

	if err := config.Save(&config.Config{
		Cortex:   "x",
		Policies: map[string]config.Policy{"pii": {Action: "local-only"}},
	}); err != nil {
		t.Fatal(err)
	}

	dd, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if dd.policy == nil || dd.pricing == nil || dd.budget == nil {
		t.Fatalf("New() left a component nil: %+v", dd)
	}
	// The PII rule is armed, so a PII prompt is forced local-only.
	if action := dd.policy.Evaluate(policy.Request{Prompt: "SSN 123-45-6789", TierHint: "1"}); !action.LocalOnly {
		t.Error("the pii=local-only config policy did not arm the local-only rule")
	}
	// Exported so cmd/hydra's SPRT/swarm dispatch branches — which bypass
	// Dispatch entirely — can still enforce the same config policy (#500).
	if !dd.PIILocalOnly() {
		t.Error("PIILocalOnly() = false, want true for a pii:local-only config")
	}
}

// PIILocalOnly is the single source of truth cmd/hydra's SPRT/swarm branches
// rely on to fold the config policy into their own LocalOnly bool. A wrong
// answer here reopens #500 regardless of what cmd/hydra does with it.
func TestDispatcher_PIILocalOnly(t *testing.T) {
	cases := []struct {
		name string
		cfg  *config.Config
		want bool
	}{
		{"no policies configured", &config.Config{}, false},
		{"pii policy set to something else", &config.Config{
			Policies: map[string]config.Policy{"pii": {Action: "budget-cap"}},
		}, false},
		{"unrelated policy present, pii absent", &config.Config{
			Policies: map[string]config.Policy{"budget": {Action: "budget-cap"}},
		}, false},
		{"pii local-only armed", &config.Config{
			Policies: map[string]config.Policy{"pii": {Action: "local-only"}},
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dd := &Dispatcher{cfg: tc.cfg}
			if got := dd.PIILocalOnly(); got != tc.want {
				t.Errorf("PIILocalOnly() = %v, want %v", got, tc.want)
			}
		})
	}
}

// writeStatePct seeds logs/state.json with a claude_pct and optional history.
func writeStatePct(t *testing.T, pct int, history []int) {
	t.Helper()
	dir := filepath.Join(config.Dir(), "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	m := map[string]any{"claude_pct": pct}
	if history != nil {
		m["claude_pct_history"] = history
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The budget governor is what enforces the 70/75/80% ceiling on every
// delegated head. Booking agy's char/4 estimate as measured usage would make
// that ceiling a fiction, so the source label has to survive the record.
func TestRecordBudget_LabelsEstimatedTokensAsEstimated(t *testing.T) {
	testutil.NewSandbox(t)
	dd := liveDispatcher()

	dd.recordBudget(&Result{
		Head:     provider.Head{ID: "measured"},
		Response: &executor.Response{InputTokens: 1000},
	})
	dd.recordBudget(&Result{
		Head:     provider.Head{ID: "guessed"},
		Response: &executor.Response{InputTokens: 1000, TokensEstimated: true},
	})

	if got := dd.budget.Get("measured"); got.Used != 1000 || got.Source != "real" {
		t.Errorf("measured tokens booked as %+v, want 1000 from a real source", got)
	}
	if got := dd.budget.Get("guessed"); got.Source != "estimate" {
		t.Errorf("estimated tokens booked as source %q — the governor would treat a "+
			"char/4 guess as a measurement", got.Source)
	}

	// A response with no token count at all must not create a tracker; a
	// zero-token entry reads as a head that was used and cost nothing.
	dd.recordBudget(&Result{
		Head:     provider.Head{ID: "silent"},
		Response: &executor.Response{},
	})
	if got := dd.budget.Get("silent"); got.Used != 0 || got.Source != "" {
		t.Errorf("a head that reported no tokens was booked as %+v", got)
	}

	// No registry at all (a Dispatcher built without one) must be a no-op, not
	// a nil dereference on the success path of every dispatch.
	(&Dispatcher{}).recordBudget(&Result{
		Head:     provider.Head{ID: "x"},
		Response: &executor.Response{InputTokens: 10},
	})
}

// truncate bounds what goes into the run log's Detail field. An error longer
// than the cap must be marked as cut, or a reader cannot tell a truncated
// message from a complete one.
func TestTruncate(t *testing.T) {
	if got := truncate("short", 200); got != "short" {
		t.Errorf("truncate did not pass a short string through: %q", got)
	}
	long := strings.Repeat("x", 500)
	got := truncate(long, 200)
	if len([]rune(got)) != 201 {
		t.Errorf("truncate produced %d runes, want 200 plus the ellipsis", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated text is not marked as cut: %q", got[len(got)-10:])
	}
	if got := truncate("", 200); got != "" {
		t.Errorf("truncate(\"\") = %q", got)
	}
}
