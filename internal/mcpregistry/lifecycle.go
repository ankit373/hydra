// SPDX-License-Identifier: MIT

package mcpregistry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
// the server's registry Name.
type ServerState struct {
	State          LifecycleState `json:"state"`
	ManifestHash   string         `json:"manifest_hash"`
	FirstSeenAt    time.Time      `json:"first_seen_at"`
	StateChangedAt time.Time      `json:"state_changed_at"`
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

// severeSecuritySignal reports whether score's Security Implementation
// category carries a signal severe enough to force quarantine (a known-bad
// match, or a flagged near-duplicate identifier) — the automaton's trigger
// for NEW/PROVISIONAL -> QUARANTINED.
func severeSecuritySignal(s Score) bool {
	for _, sig := range s.SecurityImplementation.Signals {
		if sig.Available && sig.Impact <= -40 {
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
		return ServerState{State: state, ManifestHash: hash, FirstSeenAt: now, StateChangedAt: now}
	}

	next := *prev
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
