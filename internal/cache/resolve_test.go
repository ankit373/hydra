// SPDX-License-Identifier: MIT

package cache

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/config"
)

// Off unless someone chose it, and a nil config is not a choice.
func TestEnabled_OffByDefault(t *testing.T) {
	if Enabled(nil) {
		t.Error("the cache is on with no config at all")
	}
	if Enabled(&config.Config{}) {
		t.Error("the cache is on in a config that never mentions it")
	}
	if !Enabled(&config.Config{CacheAnswers: true}) {
		t.Error("the cache stayed off after being turned on")
	}
}

// A threshold outside (0,1] is ignored rather than clamped: 0 would serve
// every prompt from whatever sits nearest it, which is the failure this whole
// package is arranged against.
func TestThreshold_RefusesUnusableValues(t *testing.T) {
	for _, bad := range []float64{0, -1, 1.5} {
		cfg := &config.Config{CacheAnswers: true, CacheThreshold: bad}
		if got := Threshold(cfg, ""); got != DefaultThreshold {
			t.Errorf("threshold %v was accepted as %v, want the default", bad, got)
		}
	}
	cfg := &config.Config{CacheAnswers: true, CacheThreshold: 0.99}
	if got := Threshold(cfg, ""); got != 0.99 {
		t.Errorf("threshold = %v, want the configured 0.99", got)
	}
}

// Per enum, because the enums are not equally forgiving.
func TestThreshold_PerEnumOverridesTheDefault(t *testing.T) {
	cfg := &config.Config{
		CacheAnswers:    true,
		CacheThreshold:  0.95,
		CacheThresholds: map[string]float64{"CORE": 0.99, "GRUNT": 0},
	}
	if got := Threshold(cfg, "CORE"); got != 0.99 {
		t.Errorf("CORE = %v, want 0.99", got)
	}
	if got := Threshold(cfg, "core"); got != 0.99 {
		t.Errorf("core = %v: the enum key is not case sensitive anywhere else", got)
	}
	if got := Threshold(cfg, "GRUNT"); got != 0.95 {
		t.Errorf("GRUNT = %v: an unusable per-enum value must fall back, not disable the gate", got)
	}
	if got := Threshold(cfg, "SIMPLE"); got != 0.95 {
		t.Errorf("an unlisted enum = %v, want the configured default", got)
	}
}

func TestBudget_DefaultsAndOverrides(t *testing.T) {
	if got := Budget(nil); got != DefaultBudgetBytes {
		t.Errorf("budget = %d with no config, want the default", got)
	}
	if got := Budget(&config.Config{CacheBudgetMB: 4}); got != 4<<20 {
		t.Errorf("budget = %d, want 4 MB", got)
	}
	if got := Budget(&config.Config{CacheBudgetMB: -1}); got != DefaultBudgetBytes {
		t.Errorf("a negative budget was accepted as %d", got)
	}
}

// Each refusal names itself, because the reason belongs beside the routing
// decision rather than in a log nobody reads.
func TestServable_RefusesWithAReason(t *testing.T) {
	cases := []struct {
		name string
		req  Request
		want string
	}{
		{"personal data", Request{PII: true}, "personal data"},
		{"injection marker", Request{Injection: true}, "injection"},
		{"pinned head", Request{Pinned: true}, "pinned"},
		{"caller opted out", Request{NoCache: true}, "fresh answer"},
	}
	for _, c := range cases {
		ok, reason := Servable(c.req)
		if ok {
			t.Errorf("%s: served anyway", c.name)
			continue
		}
		if !strings.Contains(reason, c.want) {
			t.Errorf("%s: reason %q does not mention %q", c.name, reason, c.want)
		}
	}
	if ok, reason := Servable(Request{}); !ok {
		t.Errorf("an ordinary dispatch was refused: %s", reason)
	}
}

// Off means nil everywhere downstream, so no caller has to ask twice.
func TestOpen_OffReturnsNoStore(t *testing.T) {
	s, err := Open(&config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if s != nil {
		t.Error("a store was opened with the cache off")
	}
}

// On means a store under the Hydra home, carrying the configured budget.
func TestOpen_OnBuildsAStoreWithTheConfiguredBudget(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	s, err := Open(&config.Config{CacheAnswers: true, CacheBudgetMB: 2})
	if err != nil {
		t.Fatal(err)
	}
	if s == nil {
		t.Fatal("the cache is on and no store was opened")
	}
	if s.budget != 2<<20 {
		t.Errorf("budget = %d, want the configured 2 MB", s.budget)
	}
}
