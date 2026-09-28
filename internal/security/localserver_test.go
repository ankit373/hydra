// SPDX-License-Identifier: MIT

package security

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
)

func TestLocalServerCheck_NoLocalServerIsNotEvaluated(t *testing.T) {
	c := localServerCheck(0, nil)
	if c.Status != "not evaluated" {
		t.Errorf("Status = %q, want not evaluated", c.Status)
	}
}

// The distinction that keeps this honest: a caller that did not scan must not
// read as a clean bill of health for a machine that has servers to scan.
func TestLocalServerCheck_NotScanningIsNotAPass(t *testing.T) {
	c := localServerCheck(3, nil)
	if c.Status == "loopback only" || strings.Contains(c.Status, "exposed") {
		t.Errorf("Status = %q, want something that says nothing was looked at", c.Status)
	}
	if c.Status != "not scanned" {
		t.Errorf("Status = %q, want not scanned", c.Status)
	}
	if !strings.Contains(c.Detail, "hyctl security") {
		t.Errorf("Detail %q does not say where the scan does happen", c.Detail)
	}
}

func TestLocalServerCheck_AnExposedServerNamesTheAddressAndVersion(t *testing.T) {
	c := localServerCheck(2, []LocalServer{
		{Endpoint: "http://localhost:11434", Kind: "ollama", Version: "0.16.0", OffHost: "192.168.1.14", Tried: 3},
	})
	if c.Status != "1 exposed" {
		t.Fatalf("Status = %q, want 1 exposed", c.Status)
	}
	for _, want := range []string{"192.168.1.14", "ollama", "0.16.0", "127.0.0.1"} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("Detail does not mention %q: %s", want, c.Detail)
		}
	}
}

// A version that could not be read must read as unread, never as blank-and-fine.
func TestLocalServerCheck_AnUnreadVersionSaysSo(t *testing.T) {
	c := localServerCheck(1, []LocalServer{
		{Endpoint: "http://localhost:1234", Kind: "lmstudio", OffHost: "10.0.0.5", Tried: 1},
	})
	if !strings.Contains(c.Detail, "version unread") {
		t.Errorf("Detail %q does not say the version could not be read", c.Detail)
	}
}

// The negative result is weaker evidence than the positive one, and has to say
// so: a host firewall that drops the probe produces exactly this.
func TestLocalServerCheck_LoopbackOnlyStatesTheFirewallCaveat(t *testing.T) {
	c := localServerCheck(1, []LocalServer{
		{Endpoint: "http://localhost:11434", Kind: "ollama", Version: "0.33.2", Tried: 5},
	})
	if c.Status != "loopback only" {
		t.Fatalf("Status = %q, want loopback only", c.Status)
	}
	if !strings.Contains(c.Detail, "firewall") {
		t.Errorf("Detail %q claims loopback-only without admitting what else produces it", c.Detail)
	}
}

// With nowhere to probe from, "not exposed" was never established. Reporting it
// as loopback-only would be the vacuous pass this check exists to avoid.
func TestLocalServerCheck_NothingToProbeFromRulesNothingOut(t *testing.T) {
	c := localServerCheck(1, []LocalServer{
		{Endpoint: "http://localhost:11434", Kind: "ollama", Version: "0.33.2", Tried: 0},
	})
	if c.Status != "not evaluated" {
		t.Errorf("Status = %q, want not evaluated", c.Status)
	}
	if !strings.Contains(c.Detail, "nothing was ruled out") {
		t.Errorf("Detail %q does not say the result establishes nothing", c.Detail)
	}
}

// One exposed server among several must not be hidden by the others passing,
// the same reasoning waterfall.Verdict uses for scores.
func TestLocalServerCheck_OneExposedAmongManyStillReports(t *testing.T) {
	c := localServerCheck(4, []LocalServer{
		{Endpoint: "http://localhost:1234", Kind: "lmstudio", Tried: 2},
		{Endpoint: "http://localhost:11434", Kind: "ollama", Version: "0.16.0", OffHost: "192.168.1.14", Tried: 2},
		{Endpoint: "http://localhost:8080", Kind: "llamacpp", Version: "b4567", Tried: 2},
	})
	if c.Status != "1 exposed" {
		t.Errorf("Status = %q, want 1 exposed", c.Status)
	}
	if !strings.Contains(c.Detail, "192.168.1.14") {
		t.Errorf("Detail does not name the exposed address: %s", c.Detail)
	}
}

func TestLocalHeads_CountsOnlyWhatWasFoundByDialling(t *testing.T) {
	got := localHeads([]provider.Head{
		{ID: "ollama/a", Source: "port"},
		{ID: "lmstudio/b", Source: "port"},
		{ID: "claude", Source: "cli"},
		{ID: "gpt-4", Source: "env"},
	})
	if got != 2 {
		t.Errorf("localHeads = %d, want 2", got)
	}
}

// Build must reach the check, or it renders nowhere however right it is.
func TestBuildWith_CarriesTheLocalServerCheck(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	rep, err := BuildWith(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, c := range rep.Checks {
		if c.Name == "Local server exposure" {
			found = true
		}
	}
	if !found {
		t.Error("the report carries no local server check")
	}
}
