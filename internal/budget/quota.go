// SPDX-License-Identifier: MIT

package budget

import (
	"encoding/json"
	"math"
	"sort"
)

// SessionClaudeCode is the scope the orchestrator protocol's legacy key feeds.
const SessionClaudeCode = "session:claude-code"

// The legacy keys stay because the orchestrator protocol, the TUI and the
// desktop app still write or read them; nothing outside this file names them.
const (
	legacyPctKey     = "claude_pct"
	legacyHistoryKey = "claude_pct_history"
)

// Quota is one scope's pressure. Session scopes only for now; limits in USD or
// tokens arrive with the cost ledger that would enforce them.
type Quota struct {
	Scope       string  `json:"-"`
	ContextFrac float64 `json:"context_frac"`      // 0..1
	History     []int   `json:"history,omitempty"` // percent points, newest last, ≤ MaxPctHistory
}

// Pct clamps here so a hand-written out-of-range value cannot reach a caller.
func (q Quota) Pct() int {
	return min(max(int(math.Round(q.ContextFrac*100)), 0), 100)
}

// Mode is the rate-aware governor mode for this quota.
func (q Quota) Mode() Mode {
	_, risk := q.Burn()
	return EffectiveMode(q.Pct(), risk)
}

// Burn is the burn rate in points per observation and the first-passage risk.
func (q Quota) Burn() (ratePct, risk float64) {
	return RiskFromHistory(q.History)
}

// Quotas reads every quota in a state.json body: the `quotas` key, then the
// legacy alias, which wins for SessionClaudeCode because the protocol still
// writes it. Sorted by Scope; malformed JSON yields nil.
func Quotas(state []byte) []Quota {
	var m map[string]any
	if err := json.Unmarshal(state, &m); err != nil {
		return nil
	}
	// A malformed `quotas` key leaves the alias; a malformed entry drops only itself.
	var s struct {
		Quotas map[string]json.RawMessage `json:"quotas"`
	}
	_ = json.Unmarshal(state, &s)
	byScope := map[string]Quota{}
	for scope, raw := range s.Quotas {
		var q Quota
		if json.Unmarshal(raw, &q) != nil {
			continue
		}
		q.Scope = scope
		byScope[scope] = q
	}
	if q, ok := LegacySessionQuota(m); ok {
		byScope[q.Scope] = q
	}
	out := make([]Quota, 0, len(byScope))
	for _, q := range byScope {
		out = append(out, q)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Scope < out[j].Scope })
	return out
}

// Governing is the most urgent quota (highest Mode, ties by Scope), ok=false
// when there are none.
func Governing(qs []Quota) (Quota, bool) {
	var best Quota
	var bestMode Mode
	found := false
	for _, q := range qs {
		m := q.Mode()
		if !found || m > bestMode || (m == bestMode && q.Scope < best.Scope) {
			best, bestMode, found = q, m, true
		}
	}
	return best, found
}

// LegacySessionQuota derives SessionClaudeCode from claude_pct and its history
// on every read, so the next `jq` write needs no migration step.
func LegacySessionQuota(state map[string]any) (Quota, bool) {
	pct, ok := state[legacyPctKey].(float64)
	if !ok {
		return Quota{}, false
	}
	return Quota{
		Scope:       SessionClaudeCode,
		ContextFrac: pct / 100,
		History:     asIntSlice(state[legacyHistoryKey]),
	}, true
}

// MirrorLegacyHistory appends the orchestrator's level to its bounded history,
// so the rate-aware governor and the frozen legacy readers see the trajectory.
func MirrorLegacyHistory(state map[string]any) {
	if pct := asInt(state[legacyPctKey]); pct > 0 {
		state[legacyHistoryKey] = AppendPctHistory(
			asIntSlice(state[legacyHistoryKey]), pct, MaxPctHistory)
	}
}

// asInt coerces a value decoded from JSON (numbers arrive as float64) to an int.
func asInt(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	}
	return 0
}

// asIntSlice coerces a JSON-decoded array (elements arrive as float64) to []int.
func asIntSlice(v any) []int {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]int, 0, len(raw))
	for _, e := range raw {
		out = append(out, asInt(e))
	}
	return out
}
