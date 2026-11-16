// SPDX-License-Identifier: MIT

package idle

import (
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/budget"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/sysinfo"
)

var clock = time.Date(2026, 11, 16, 3, 0, 0, 0, time.UTC)

type world struct {
	conv    Conversation
	heads   []provider.Head
	windows map[string]int
	machine Machine
}

// ready is an idle, full conversation on a free machine with a local model.
func ready() world {
	return world{
		conv: Conversation{Key: "c1", LastEvent: clock.Add(-10 * time.Minute),
			Quota: budget.Quota{ContextFrac: 0.62}, NewTokens: 40000},
		heads: []provider.Head{
			{ID: "api/big", LocalOnly: false, AuthReady: true},
			{ID: "ollama/small", LocalOnly: true, AuthReady: true},
			{ID: "ollama/large", LocalOnly: true, AuthReady: true},
		},
		windows: map[string]int{"api/big": 200000, "ollama/small": 4096, "ollama/large": 32768},
		machine: Machine{OnAC: true, PowerKnown: true, Load: 1, LoadKnown: true, CPUs: 8,
			Pressure: sysinfo.PressureLow},
	}
}

func (w world) eligible() (Job, bool, string) {
	return Eligible(w.conv, w.heads, w.windows, w.machine, clock, Config{})
}

func refuses(t *testing.T, w world, want string) {
	t.Helper()
	if _, ok, why := w.eligible(); ok || !strings.Contains(why, want) {
		t.Fatalf("got ok=%v reason %q, want a refusal naming %q", ok, why, want)
	}
}

func TestEligible_IdleFullConversationOnACIsEligible(t *testing.T) {
	job, ok, why := ready().eligible()
	if !ok {
		t.Fatalf("refused: %s", why)
	}
	if job.Conv != "c1" || job.Head.ID != "ollama/large" || job.Window != 32768 || job.Tokens != 40000 {
		t.Fatalf("job %+v, want c1 on the largest local window", job)
	}
}

func TestEligible_RecentEventIsNotIdle(t *testing.T) {
	w := ready()
	w.conv.LastEvent = clock.Add(-30 * time.Second)
	refuses(t, w, "active 30s ago")
}

func TestEligible_LowFillNotWorthIt(t *testing.T) {
	w := ready()
	w.conv.Quota = budget.Quota{ContextFrac: 0.2}
	refuses(t, w, "20% full")

	w.conv.Quota.History = []int{10, 30, 50, 70} // filling fast
	if _, ok, why := w.eligible(); !ok {
		t.Fatalf("a fast burn should be worth it at low fill: %s", why)
	}

	w = ready()
	w.conv.NewTokens = 500
	refuses(t, w, "500 new tokens")
}

func TestEligible_NoLocalHeadRefuses(t *testing.T) {
	w := ready()
	w.heads = w.heads[:2] // the remote one and a 4096 window
	refuses(t, w, "no local model")
}

func TestEligible_OnBatteryRefuses(t *testing.T) {
	w := ready()
	w.machine.OnAC = false
	refuses(t, w, "on battery")
	w.machine.PowerKnown = false
	refuses(t, w, "power source unknown")
}

func TestEligible_BusyMachineRefuses(t *testing.T) {
	w := ready()
	w.machine.Load = 6
	refuses(t, w, "load 0.75 per CPU")
	w.machine.LoadKnown = false
	refuses(t, w, "load unknown")
	w = ready()
	w.machine.Pressure = sysinfo.PressureModerate
	refuses(t, w, "memory pressure moderate")
}

func TestEligible_ConfigOverridesDefaults(t *testing.T) {
	w := ready()
	cfg := Config{IdleAfter: time.Hour, MinFillPct: 90, MinRisk: 0.9, MinSpanTokens: 100000, MaxLoadPerCPU: 0.1}
	if got := cfg.orDefault(); got != cfg {
		t.Fatalf("orDefault replaced set fields: %+v", got)
	}
	if _, ok, why := Eligible(w.conv, w.heads, w.windows, w.machine, clock, cfg); ok || !strings.Contains(why, "idle after 1h0m0s") {
		t.Fatalf("got ok=%v %q, want the hour's idle bar", ok, why)
	}
}
