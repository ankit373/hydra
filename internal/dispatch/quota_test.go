// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ankit373/hydra/internal/budget"
	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

// The governor's whole purpose is to downgrade before the orchestrator runs
// out, so each band's arithmetic is pinned against what claudeMode did.
func TestQuotaMode_DowngradesLikeClaudeMode(t *testing.T) {
	bands := []struct {
		pct  int
		mode budget.Mode
		want map[string]string // hint → tier
	}{
		{10, budget.ModeNormal, map[string]string{"": "", "3": "3", "9": "9", "10": "10"}},
		{55, budget.ModeCompact, map[string]string{"": "", "3": "3", "9": "9", "10": "10"}},
		{66, budget.ModeCaution, map[string]string{"": "", "3": "3", "9": "9", "10": "10"}},
		{72, budget.ModeWarning, map[string]string{"": "", "3": "4", "9": "10", "10": "10"}},
		{77, budget.ModeCritical, map[string]string{"": "", "3": "5", "9": "10", "10": "10"}},
		{85, budget.ModeEmergency, map[string]string{"": "", "3": "10", "9": "10", "10": "10"}},
	}
	for _, b := range bands {
		for hint, wantTier := range b.want {
			t.Run(fmt.Sprintf("%s/hint=%q", b.mode, hint), func(t *testing.T) {
				testutil.NewSandbox(t)
				writeStatePct(t, b.pct, nil)
				tier, q, mode := liveDispatcher().quotaMode(hint)
				if mode != b.mode || tier != wantTier {
					t.Errorf("at %d%% hint %q: mode=%s tier=%q, want mode=%s tier=%q",
						b.pct, hint, mode, tier, b.mode, wantTier)
				}
				if q.Scope != budget.SessionClaudeCode || q.Pct() != b.pct {
					t.Errorf("governing quota = %s at %d%%, want %s at %d%%",
						q.Scope, q.Pct(), budget.SessionClaudeCode, b.pct)
				}
			})
		}
	}
}

// G1: a state.json in the pre-E1 shape routes exactly as claudeMode routed it.
// The expected values were recorded by running claudeMode on these files.
func TestQuotaMode_GoldenLegacyStateRoutesAsBefore(t *testing.T) {
	for _, tc := range []struct {
		file, hint, oldMode, oldTier string
	}{
		{"state_pre_e1_burning.json", "", "critical", ""},
		{"state_pre_e1_burning.json", "3", "critical", "5"},
		{"state_pre_e1_burning.json", "9", "critical", "10"},
		{"state_pre_e1_steady.json", "3", "warning", "4"},
		{"state_pre_e1_steady.json", "10", "warning", "10"},
	} {
		t.Run(tc.file+"/"+tc.hint, func(t *testing.T) {
			testutil.NewSandbox(t)
			raw, err := os.ReadFile(filepath.Join("testdata", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			writeStateRaw(t, raw)
			tier, _, mode := liveDispatcher().quotaMode(tc.hint)
			if mode.String() != tc.oldMode || tier != tc.oldTier {
				t.Errorf("legacy state.json: claudeMode gave mode=%s tier=%s, quotaMode gives mode=%s tier=%s",
					tc.oldMode, tc.oldTier, mode, tier)
			}
		})
	}
}

// G3: at 80% the session is about to stop, so the emergency branch must keep
// forcing local-only, even when no local head exists and a remote one could answer.
func TestQuotaMode_EmergencyStillForcesLocal(t *testing.T) {
	const pct = 85
	for _, hasLocal := range []bool{true, false} {
		t.Run(fmt.Sprintf("local head %v", hasLocal), func(t *testing.T) {
			s := testutil.NewSandbox(t)
			writeStatePct(t, pct, nil)
			heads := []provider.Head{echoHead(t, s, "away", 95)}
			if hasLocal {
				home := echoHead(t, s, "home", 40)
				home.LocalOnly = true
				heads = append(heads, home)
			}

			res, err := liveDispatcher(heads...).Dispatch(context.Background(), "work", Options{})
			if hasLocal && err != nil {
				t.Fatalf("dispatch failed with a working local head: %v", err)
			}
			if err == nil && !res.Head.LocalOnly {
				t.Errorf("quota at %d%% (emergency) dispatched to non-local head %s", pct, res.Head.ID)
			}
		})
	}
}

// In-process only: goroutines sharing stateMu must never write back a stale
// read and roll the level back. Another process (jq, a second hyctl) is not covered.
func TestSyncStateJSON_InProcessSyncsNeverRollTheLevelBack(t *testing.T) {
	testutil.NewSandbox(t)
	writeStatePct(t, 10, nil)
	path := filepath.Join(config.Dir(), "logs", "state.json")
	d := &Dispatcher{}

	const writers, rounds = 32, 16
	writes, last, rolledBack := 0, 10, 0 // guarded by stateMu
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range rounds {
				stateMu.Lock()
				raw, _ := os.ReadFile(path)
				m := map[string]any{}
				_ = json.Unmarshal(raw, &m)
				if m["claude_pct"] != float64(last) {
					rolledBack++
				}
				writes++
				last = 1 + writes%99
				m["claude_pct"] = last
				raw, _ = json.Marshal(m)
				_ = os.WriteFile(path, raw, 0o600)
				stateMu.Unlock()
				d.syncStateJSON(&Result{Head: provider.Head{ID: fmt.Sprintf("h%d", i)}})
			}
		}()
	}
	wg.Wait()

	if rolledBack > 0 {
		t.Errorf("%d of %d writes found the level on disk rolled back by a sync's stale write", rolledBack, writes)
	}
	state := readState(t)
	hist, _ := state["claude_pct_history"].([]any)
	if state["claude_pct"] != float64(last) || len(hist) == 0 || hist[len(hist)-1] != float64(last) {
		t.Errorf("after %d concurrent syncs the last level written was %d, but state.json holds "+
			"claude_pct=%v with history %v", writes, last, state["claude_pct"], hist)
	}
}

// The governor's lines are what users already read; these are develop's
// strings verbatim (dispatch.go at b76d105), for a tier that moved.
func TestGovernorLog_KeepsDevelopsWording(t *testing.T) {
	q := budget.Quota{Scope: budget.SessionClaudeCode, ContextFrac: 0.77}
	for mode, want := range map[budget.Mode]string{
		budget.ModeEmergency: "🚨 EMERGENCY: Claude at 77%, routing to local tier only. Start a new session.",
		budget.ModeCritical:  "🔴 CRITICAL: Claude at 77%, downgrading tier by 2. Run /compact NOW.",
		budget.ModeWarning:   "🟠 WARNING: Claude at 77%, downgrading tier by 1. Run /compact now.",
		budget.ModeCaution:   "🟡 CAUTION: Claude at 77%, Run /compact immediately.",
		budget.ModeCompact:   "ℹ️  Claude at 77%, Consider running /compact.",
		budget.ModeNormal:    "",
	} {
		if got := governorLog(q, mode, "3", "5"); got != want {
			t.Errorf("%s logged %q, want develop's %q", mode, got, want)
		}
	}
}

// A downgrade is claimed only when the tier moved: an unpinned dispatch (hint
// "") or one already at the floor has nothing to downgrade.
func TestGovernorLog_ClaimsADowngradeOnlyWhenTheTierMoved(t *testing.T) {
	q := budget.Quota{Scope: budget.SessionClaudeCode, ContextFrac: 0.77}
	for _, mode := range []budget.Mode{budget.ModeCritical, budget.ModeWarning} {
		for _, tc := range []struct{ from, to string }{{"", ""}, {"10", "10"}} {
			if got := governorLog(q, mode, tc.from, tc.to); strings.Contains(got, "downgrading") ||
				!strings.Contains(got, "Claude at 77%") {
				t.Errorf("%s, tier %q → %q logged %q, which claims a downgrade that did not happen",
					mode, tc.from, tc.to, got)
			}
		}
	}
}

// writeStateRaw seeds logs/state.json with a body exactly as found on disk.
func writeStateRaw(t *testing.T, raw []byte) {
	t.Helper()
	dir := filepath.Join(config.Dir(), "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
