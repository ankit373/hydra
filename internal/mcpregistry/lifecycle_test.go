// SPDX-License-Identifier: MIT

package mcpregistry

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func srvV(name, version string) ServerRecord {
	return ServerRecord{Name: name, Version: version, Packages: []Package{{RegistryType: "npm", Identifier: name}}}
}

func cleanScore() Score {
	return Score{SecurityImplementation: CategoryScore{Confidence: ConfidenceHigh, Signals: []Signal{
		{Name: "known-vulnerability match", Available: true, Impact: 0},
	}}}
}

func badScore() Score {
	return Score{SecurityImplementation: CategoryScore{Confidence: ConfidenceHigh, Signals: []Signal{
		{Name: "known-vulnerability match", Available: true, Impact: -100},
	}}}
}

func TestManifestHash_ChangesOnVersionBump(t *testing.T) {
	a := ManifestHash(srvV("x", "1.0.0"))
	b := ManifestHash(srvV("x", "1.0.1"))
	if a == b {
		t.Error("hash should differ when version changes")
	}
}

func TestManifestHash_StableForIdenticalInput(t *testing.T) {
	a := ManifestHash(srvV("x", "1.0.0"))
	b := ManifestHash(srvV("x", "1.0.0"))
	if a != b {
		t.Error("hash should be stable for identical input")
	}
}

func TestAdvance_NewCleanServerStartsProvisional(t *testing.T) {
	next := Advance(nil, srvV("x", "1.0.0"), cleanScore(), time.Now())
	if next.State != StateProvisional {
		t.Errorf("State = %q, want provisional", next.State)
	}
}

func TestAdvance_NewSevereServerStartsQuarantined(t *testing.T) {
	next := Advance(nil, srvV("x", "1.0.0"), badScore(), time.Now())
	if next.State != StateQuarantined {
		t.Errorf("State = %q, want quarantined", next.State)
	}
}

func TestAdvance_ProvisionalGraduatesAfterCooldownWithStableHash(t *testing.T) {
	srv := srvV("x", "1.0.0")
	now := time.Now()
	prev := ServerState{State: StateProvisional, ManifestHash: ManifestHash(srv), StateChangedAt: now.Add(-provisionalCooldown - time.Hour)}
	next := Advance(&prev, srv, cleanScore(), now)
	if next.State != StateTrusted {
		t.Errorf("State = %q, want trusted after cooldown", next.State)
	}
}

func TestAdvance_ProvisionalDoesNotGraduateBeforeCooldown(t *testing.T) {
	srv := srvV("x", "1.0.0")
	now := time.Now()
	prev := ServerState{State: StateProvisional, ManifestHash: ManifestHash(srv), StateChangedAt: now.Add(-time.Hour)}
	next := Advance(&prev, srv, cleanScore(), now)
	if next.State != StateProvisional {
		t.Errorf("State = %q, want still provisional before cooldown elapses", next.State)
	}
}

func TestAdvance_TrustedDropsToProvisionalOnVersionBump(t *testing.T) {
	old := srvV("x", "1.0.0")
	bumped := srvV("x", "1.0.1")
	prev := ServerState{State: StateTrusted, ManifestHash: ManifestHash(old), StateChangedAt: time.Now().Add(-30 * 24 * time.Hour)}
	next := Advance(&prev, bumped, cleanScore(), time.Now())
	if next.State != StateProvisional {
		t.Fatalf("State = %q, want provisional — this is the single edge that would have caught postmark-mcp", next.State)
	}
	if next.ManifestHash != ManifestHash(bumped) {
		t.Error("ManifestHash should update to the new version's hash")
	}
}

func TestAdvance_TrustedUnchangedStaysTrusted(t *testing.T) {
	srv := srvV("x", "1.0.0")
	prev := ServerState{State: StateTrusted, ManifestHash: ManifestHash(srv), StateChangedAt: time.Now().Add(-30 * 24 * time.Hour)}
	next := Advance(&prev, srv, cleanScore(), time.Now())
	if next.State != StateTrusted {
		t.Errorf("State = %q, want still trusted when nothing changed", next.State)
	}
}

func TestAdvance_TrustedFlaggedByPostHocCVE(t *testing.T) {
	srv := srvV("x", "1.0.0")
	prev := ServerState{State: StateTrusted, ManifestHash: ManifestHash(srv), StateChangedAt: time.Now().Add(-30 * 24 * time.Hour)}
	next := Advance(&prev, srv, badScore(), time.Now())
	if next.State != StateFlagged {
		t.Errorf("State = %q, want flagged — same version, but a CVE was filed after the fact", next.State)
	}
}

func TestAdvance_FlaggedReturnsToProvisionalOnceClean(t *testing.T) {
	srv := srvV("x", "1.0.0")
	prev := ServerState{State: StateFlagged, ManifestHash: ManifestHash(srv), StateChangedAt: time.Now()}
	next := Advance(&prev, srv, cleanScore(), time.Now())
	if next.State != StateProvisional {
		t.Errorf("State = %q, want provisional (re-verification path)", next.State)
	}
}

func TestAdvance_ProvisionalQuarantinedBySevereSignal(t *testing.T) {
	srv := srvV("x", "1.0.0")
	prev := ServerState{State: StateProvisional, ManifestHash: ManifestHash(srv), StateChangedAt: time.Now().Add(-time.Hour)}
	next := Advance(&prev, srv, badScore(), time.Now())
	if next.State != StateQuarantined {
		t.Errorf("State = %q, want quarantined", next.State)
	}
}

func TestAdvance_QuarantinedNeverAutoPromotes(t *testing.T) {
	srv := srvV("x", "1.0.0")
	prev := ServerState{State: StateQuarantined, ManifestHash: ManifestHash(srv), StateChangedAt: time.Now().Add(-365 * 24 * time.Hour)}
	next := Advance(&prev, srv, cleanScore(), time.Now())
	if next.State != StateQuarantined {
		t.Errorf("State = %q, want to stay quarantined — no automatic path out, that's a manual-clear decision", next.State)
	}
}

func TestAdvance_StateChangedAtOnlyUpdatesOnTransition(t *testing.T) {
	srv := srvV("x", "1.0.0")
	changedAt := time.Now().Add(-time.Hour)
	prev := ServerState{State: StateTrusted, ManifestHash: ManifestHash(srv), StateChangedAt: changedAt}
	next := Advance(&prev, srv, cleanScore(), time.Now())
	if !next.StateChangedAt.Equal(changedAt) {
		t.Errorf("StateChangedAt should be unchanged when the state doesn't transition")
	}
}

func TestLoadStates_CorruptFileIsAnError(t *testing.T) {
	withTempHydraHome(t)
	path := statePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadStates(); err == nil {
		t.Fatal("a corrupt state file must surface as an error, not silently yield an empty map")
	}
}

func TestLoadStates_MissingFileYieldsEmptyMap(t *testing.T) {
	withTempHydraHome(t)
	states, err := LoadStates()
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 0 {
		t.Errorf("expected an empty map, got %v", states)
	}
}

func TestSaveLoadStates_RoundTrip(t *testing.T) {
	withTempHydraHome(t)
	want := map[string]ServerState{
		"io.github.foo/bar": {State: StateTrusted, ManifestHash: "abc123", StateChangedAt: time.Now().UTC().Truncate(time.Second)},
	}
	if err := SaveStates(want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadStates()
	if err != nil {
		t.Fatal(err)
	}
	if got["io.github.foo/bar"].State != StateTrusted {
		t.Errorf("round-tripped state = %+v", got["io.github.foo/bar"])
	}
}
