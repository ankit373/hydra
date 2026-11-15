// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/pricing"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

func gateCodes(gates []Gate) map[string]GateCode {
	m := map[string]GateCode{}
	for _, g := range gates {
		m[g.Head] = g.Code
	}
	return m
}

func TestSelectHeads_ReportsDroppedWithReason(t *testing.T) {
	d := routingDispatcher()
	d.heads = append(d.heads, provider.Head{ID: "nodriver", Name: "nodriver", Provider: "nosuch", Source: "cli", CapScore: 50})
	d.health = parked(t, "expert", "agy not found")

	cases := []struct {
		name      string
		tier      string
		localOnly bool
		kept      []string
		dropped   map[string]GateCode
	}{
		{"tier", "7", false, []string{"mid", "weak"}, map[string]GateCode{
			"strongest": GateTier, "expert": GateParked, "nodriver": GateNoExecutor}},
		{"local only", "", true, []string{"weak"}, map[string]GateCode{
			"strongest": GateNotLocal, "expert": GateParked, "mid": GateNotLocal, "nodriver": GateNoExecutor}},
	}
	for _, c := range cases {
		heads, gates := d.gateHeads(c.tier, c.localOnly, "")
		if got := ids(heads); !slices.Equal(got, c.kept) {
			t.Errorf("%s: kept %v, want %v", c.name, got, c.kept)
		}
		if got := gateCodes(gates); !maps.Equal(got, c.dropped) {
			t.Errorf("%s: gates %v, want %v", c.name, got, c.dropped)
		}
	}

	// Nothing is cheap enough, so every live head comes back: none was
	// dropped for its tier, and saying so would contradict the list.
	d.health = parked(t, "weak", "ollama down")
	heads, gates := d.gateHeads("10", false, "")
	if len(heads) != 3 {
		t.Fatalf("degraded to %v, want the three live heads", ids(heads))
	}
	for _, g := range gates {
		if g.Code == GateTier {
			t.Errorf("degrade path reported %s dropped for its tier", g.Head)
		}
	}
}

// The case #1480 is done on: one head dropped by each kind of gate, and the
// run still answers from the head left.
func TestDispatch_RefusedCandidateHasReasonCode(t *testing.T) {
	s := testutil.NewSandbox(t)
	// A local head priced at tier 1, so a local-only run can still hit the ceiling.
	dear := provider.Head{ID: "dear", Name: "dear", Provider: "agy", Source: "registry",
		Executable: "/usr/bin/agy", LocalOnly: true, AuthReady: true, CapScore: 99,
		Meta: map[string]string{"tier": "1"}}
	sick := echoHead(t, s, "sick", 98)
	sick.LocalOnly = true
	remote := echoHead(t, s, "remote", 97)
	answer := echoHead(t, s, "answer", 50)
	answer.LocalOnly = true

	d := liveDispatcher(dear, sick, remote, answer)
	d.pricing = pricing.Load()
	d.health = parked(t, "sick", "binary crashed")

	res, err := d.Dispatch(context.Background(), strings.Repeat("x", 400), Options{
		LocalOnly: true, MaxCostUSD: 1e-7, RunID: "run-gates", TaskID: "task-gates",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Head.ID != "answer" {
		t.Fatalf("answered by %s, want answer", res.Head.ID)
	}
	want := map[string]GateCode{"sick": GateParked, "remote": GateNotLocal, "dear": GateCostCeiling}
	if got := gateCodes(res.Gates); !maps.Equal(got, want) {
		t.Errorf("gates %+v, want %v", res.Gates, want)
	}
}

// Readers key on the code, so its value is pinned here; the human sentence is
// free to change, and the refusal text an Attempt carries is the same one.
func TestDispatch_GateCodesStableAcrossMessageChanges(t *testing.T) {
	pinned := map[GateCode]string{
		GateNoExecutor: "no_executor", GateParked: "parked", GateNotLocal: "not_local",
		GateTier: "tier", GateTools: "tools", GateTemperature: "temperature",
		GateEgress: "egress", GateLedgerDeny: "ledger_deny", GateLedgerAsk: "ledger_ask",
		GateLedgerError: "ledger_error", GateCostCeiling: "cost_ceiling",
	}
	for code, want := range pinned {
		if string(code) != want {
			t.Errorf("gate code %q changed, want %q", code, want)
		}
	}

	s := testutil.NewSandbox(t)
	answer := echoHead(t, s, "answer", 50)
	d := liveDispatcher(cappedHead(t), answer)
	warm := 1.5
	res, err := d.Dispatch(context.Background(), "hi", Options{Temperature: &warm, RunID: "run-code", TaskID: "task-code"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Gates) != 1 || res.Gates[0].Code != GateTemperature {
		t.Fatalf("gates %+v, want one temperature gate", res.Gates)
	}
	if len(res.Attempts) != 1 || res.Attempts[0].Reason != res.Gates[0].Detail {
		t.Errorf("attempt %+v and gate %+v disagree on the reason", res.Attempts, res.Gates[0])
	}
	if !strings.HasPrefix(res.Gates[0].Detail, "accepts a temperature of at most") {
		t.Errorf("refusal text changed: %q", res.Gates[0].Detail)
	}
}
