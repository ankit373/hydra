// SPDX-License-Identifier: MIT

package mcpregistry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ankit373/hydra/internal/config"
)

// LifecycleState is a server's position in the trust automaton (design doc
// §9): every version bump costs a server its accumulated trust until it
// re-earns a cooldown period — the direct, mechanical fix for the
// postmark-mcp rug-pull pattern (15 clean versions, then a malicious one).
type LifecycleState string

const (
	StateNew         LifecycleState = "new"
	StateProvisional LifecycleState = "provisional"
	StateTrusted     LifecycleState = "trusted"
	StateFlagged     LifecycleState = "flagged"
	StateQuarantined LifecycleState = "quarantined"
	StateDelisted    LifecycleState = "delisted"
)

// provisionalCooldown is how long a server must hold a stable manifest hash
// before graduating provisional -> trusted. A starting default, not a value
// derived from a specific cited study (unlike the Cox hazard ratios in
// score.go) — reasonable, not claimed as more rigorous than it is.
const provisionalCooldown = 14 * 24 * time.Hour

// ServerState is what's persisted per server between audit runs, keyed by
// the server's registry Name. LastScore is the most recent ComputeScore
// result — persisted so a later export or report doesn't have to re-run
// the network-calling signals just to redisplay a score already computed.
type ServerState struct {
	State          LifecycleState `json:"state"`
	ManifestHash   string         `json:"manifest_hash"`
	FirstSeenAt    time.Time      `json:"first_seen_at"`
	StateChangedAt time.Time      `json:"state_changed_at"`
	LastScore      Score          `json:"last_score"`
}

// ManifestHash fingerprints the parts of a server record that matter for
// re-verification: identity, version, and what it ships. Any change here —
// not a statistically-detected anomaly — is what triggers re-review; the
// design doc's research (§12) found the published defense for rug-pulls is
// hash-pinning plus forced re-approval, not a learned changepoint detector.
func ManifestHash(srv ServerRecord) string {
	h := sha256.New()
	h.Write([]byte(srv.Name))
	h.Write([]byte("\x00"))
	h.Write([]byte(srv.Version))
	for _, p := range srv.Packages {
		h.Write([]byte("\x00"))
		h.Write([]byte(p.RegistryType))
		h.Write([]byte(":"))
		h.Write([]byte(p.Identifier))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// quarantineThreshold is deliberately below the near-duplicate signal's
// -40: only a *confirmed* finding (a known-bad advisory match, -100) puts a
// server in quarantine, because quarantine has no automatic way out. A
// name-similarity heuristic is not a confirmation — measured against the
// live registry it flagged 0.7% of servers and every single one of those was
// a false positive, so wiring it to an unrecoverable state would have
// stranded roughly 550 legitimately-published servers. It still subtracts
// from the score; it just doesn't condemn.
const quarantineThreshold = -80.0

// severeSecuritySignal reports whether score's Security Implementation
// category carries a confirmed finding severe enough to force quarantine —
// the automaton's trigger for NEW/PROVISIONAL -> QUARANTINED.
func severeSecuritySignal(s Score) bool {
	for _, sig := range s.SecurityImplementation.Signals {
		if sig.Available && sig.Impact <= quarantineThreshold {
			return true
		}
	}
	return false
}

// Advance computes a server's next lifecycle state. prev is nil the first
// time a server is seen. now is passed in rather than read from time.Now()
// so transition logic is deterministically testable.
func Advance(prev *ServerState, srv ServerRecord, score Score, now time.Time) ServerState {
	hash := ManifestHash(srv)
	quarantineTriggered := severeSecuritySignal(score)

	if prev == nil {
		state := StateProvisional
		if quarantineTriggered {
			state = StateQuarantined
		}
		return ServerState{State: state, ManifestHash: hash, FirstSeenAt: now, StateChangedAt: now, LastScore: score}
	}

	next := *prev
	// Set here, not by the caller: LastScore is what `export` publishes, and
	// leaving it to each call site meant one that forgot would carry a stale
	// score into the public directory.
	next.LastScore = score
	switch prev.State {
	case StateTrusted:
		switch {
		case hash != prev.ManifestHash:
			next.State = StateProvisional
		case quarantineTriggered:
			next.State = StateFlagged
		}
	case StateProvisional:
		switch {
		case quarantineTriggered:
			next.State = StateQuarantined
		case hash == prev.ManifestHash && now.Sub(prev.StateChangedAt) >= provisionalCooldown:
			next.State = StateTrusted
		}
	case StateFlagged:
		if !quarantineTriggered {
			next.State = StateProvisional
		}
	case StateQuarantined, StateDelisted, StateNew:
		// No automatic promotion out of quarantine/delisted — that's a
		// manual-clear path (design doc §9's "false positive, manually
		// cleared" edge), not something an audit run decides on its own.
		// StateNew never persists (Advance always assigns Provisional or
		// Quarantined on first sight) but is handled here defensively.
	}
	if next.State != prev.State {
		next.StateChangedAt = now
	}
	next.ManifestHash = hash
	return next
}

// ErrNotQuarantined is returned by Clear for a server that isn't in a state
// a manual clear applies to.
var ErrNotQuarantined = errors.New("server is not quarantined or delisted")

// Clear is the manual path out of quarantine — the "false positive, manually
// cleared" edge the automaton documents. Advance deliberately never takes
// this edge on its own; without a way to invoke it, a wrongly-quarantined
// server was unrecoverable short of hand-editing the state file. Returns the
// state it moved to, and resets the cooldown clock so a cleared server
// re-earns trust rather than being handed it back.
func Clear(name string, now time.Time) (LifecycleState, error) {
	states, err := LoadStates()
	if err != nil {
		return "", err
	}
	st, ok := states[name]
	if !ok {
		return "", fmt.Errorf("no recorded state for %q — run `hyctl mcp registry audit` first", name)
	}
	if st.State != StateQuarantined && st.State != StateDelisted {
		return st.State, ErrNotQuarantined
	}
	st.State = StateProvisional
	st.StateChangedAt = now
	states[name] = st
	if err := SaveStates(states); err != nil {
		return "", err
	}
	return st.State, nil
}

func statePath() string {
	return filepath.Join(config.Dir(), "mcp_registry_state.json")
}

// LoadStates reads the persisted lifecycle-state map. A missing file yields
// an empty map, not an error — the very first audit run has no history yet.
func LoadStates() (map[string]ServerState, error) {
	raw, err := os.ReadFile(statePath())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]ServerState{}, nil
		}
		return nil, err
	}
	var states map[string]ServerState
	if err := json.Unmarshal(raw, &states); err != nil {
		return nil, err
	}
	if states == nil {
		states = map[string]ServerState{}
	}
	return states, nil
}

// SaveStates writes the lifecycle-state map atomically.
func SaveStates(states map[string]ServerState) error {
	raw, err := json.MarshalIndent(states, "", "  ")
	if err != nil {
		return err
	}
	path := statePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	defer func() { _ = os.Remove(tmp) }()
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return os.WriteFile(path, raw, 0o600)
	}
	return nil
}
