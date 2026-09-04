// SPDX-License-Identifier: MIT

package mcpregistry

import (
	"errors"
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

func typosquatFlaggedScore() Score {
	return Score{SecurityImplementation: CategoryScore{Confidence: ConfidenceHigh, Signals: []Signal{
		{Name: "near-duplicate identifier", Available: true, Impact: -40},
	}}}
}

// A name-similarity heuristic is not a confirmation, and quarantine has no
// automatic way out. Measured against the live registry the heuristic flagged
// 0.7% of servers and every one was a false positive, so it must lower the
// score without condemning the server.
func TestAdvance_TyposquatFlagDoesNotQuarantine(t *testing.T) {
	next := Advance(nil, srvV("x", "1.0.0"), typosquatFlaggedScore(), time.Now())
	if next.State == StateQuarantined {
		t.Error("a near-duplicate-name heuristic must not put a server in an unrecoverable state on first sight")
	}
	if next.State != StateProvisional {
		t.Errorf("State = %q, want provisional", next.State)
	}
}

func TestAdvance_ConfirmedKnownBadStillQuarantines(t *testing.T) {
	next := Advance(nil, srvV("x", "1.0.0"), badScore(), time.Now())
	if next.State != StateQuarantined {
		t.Errorf("State = %q, want quarantined — a confirmed advisory match is a confirmation", next.State)
	}
}

func TestAdvance_RecordsTheScoreThatProducedTheState(t *testing.T) {
	score := cleanScore()
	score.Overall = 87
	first := Advance(nil, srvV("x", "1.0.0"), score, time.Now())
	if first.LastScore.Overall != 87 {
		t.Errorf("first sight: LastScore.Overall = %v, want 87 — the type must own this, not the caller", first.LastScore.Overall)
	}
	next := cleanScore()
	next.Overall = 42
	after := Advance(&first, srvV("x", "1.0.0"), next, time.Now())
	if after.LastScore.Overall != 42 {
		t.Errorf("subsequent run: LastScore.Overall = %v, want 42 (stale score would be published by export)", after.LastScore.Overall)
	}
}

func TestClear_MovesQuarantinedBackToProvisionalAndResetsCooldown(t *testing.T) {
	withTempHydraHome(t)
	longAgo := time.Now().Add(-365 * 24 * time.Hour)
	if err := SaveStates(map[string]ServerState{
		"io.github.x/y": {State: StateQuarantined, ManifestHash: "h", StateChangedAt: longAgo},
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	state, err := Clear("io.github.x/y", now)
	if err != nil {
		t.Fatal(err)
	}
	if state != StateProvisional {
		t.Errorf("Clear returned %q, want provisional", state)
	}
	states, err := LoadStates()
	if err != nil {
		t.Fatal(err)
	}
	got := states["io.github.x/y"]
	if got.State != StateProvisional {
		t.Errorf("persisted state = %q, want provisional", got.State)
	}
	// The cooldown clock must restart, or a cleared server is instantly
	// promoted to trusted on the next audit by its year-old timestamp.
	if !got.StateChangedAt.Equal(now) {
		t.Errorf("StateChangedAt = %v, want the clear time %v — a cleared server must re-earn trust", got.StateChangedAt, now)
	}
}

func TestClear_RefusesAServerThatIsNotQuarantined(t *testing.T) {
	withTempHydraHome(t)
	if err := SaveStates(map[string]ServerState{"a/b": {State: StateTrusted}}); err != nil {
		t.Fatal(err)
	}
	state, err := Clear("a/b", time.Now())
	if !errors.Is(err, ErrNotQuarantined) {
		t.Fatalf("err = %v, want ErrNotQuarantined", err)
	}
	if state != StateTrusted {
		t.Errorf("should report the actual state (%q) so the message can name it", state)
	}
}

func TestClear_UnknownServerIsAnError(t *testing.T) {
	withTempHydraHome(t)
	if _, err := Clear("never/audited", time.Now()); err == nil {
		t.Fatal("clearing a server with no recorded state must be an error, not a silent no-op")
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
