// SPDX-License-Identifier: MIT

package budget

import (
	"encoding/json"
	"log"
	"math"
	"sort"
	"sync"
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
	Scope       string
	ContextFrac float64 // 0..1
	History     []int   // percent points, newest last, ≤ MaxPctHistory
}

// Pct is the level in whole percent, rounded the way the history is recorded.
func (q Quota) Pct() int { return roundPct(q.ContextFrac * 100) }

// Mode is the rate-aware governor mode for this quota.
func (q Quota) Mode() Mode {
	_, risk := q.Burn()
	return EffectiveMode(q.Pct(), risk)
}

// Burn is the burn rate in points per observation and the first-passage risk.
func (q Quota) Burn() (ratePct, risk float64) {
	return RiskFromHistory(q.History)
}

// State is state.json decoded once, for the governor and `hyctl status` alike.
type State struct {
	Quotas []Quota // sorted by Scope
	budget json.RawMessage
}

// Budget is the per-model snapshot table keyed by head ID, decoded on demand
// because the dispatch path never reads it. A non-object entry is skipped.
func (s State) Budget() map[string]map[string]any {
	var entries map[string]json.RawMessage
	_ = json.Unmarshal(s.budget, &entries)
	out := map[string]map[string]any{}
	for id, raw := range entries {
		var snap map[string]any
		if json.Unmarshal(raw, &snap) == nil && snap != nil {
			out[id] = snap
		}
	}
	return out
}

// ParseState reads a state.json body. A field of the wrong type reads as
// absent rather than failing the file; malformed JSON yields the zero State.
func ParseState(raw []byte) State {
	var w struct {
		Pct     any             `json:"claude_pct"`
		History any             `json:"claude_pct_history"`
		Quotas  json.RawMessage `json:"quotas"`
		Budget  json.RawMessage `json:"budget"`
	}
	if json.Unmarshal(raw, &w) != nil {
		return State{}
	}
	s := State{budget: w.Budget}

	var entries map[string]json.RawMessage
	_ = json.Unmarshal(w.Quotas, &entries)
	byScope := map[string]Quota{}
	for scope, raw := range entries {
		if q, ok := parseQuota(scope, raw); ok {
			byScope[scope] = q
		}
	}
	// The alias wins: the orchestrator rewrites claude_pct every turn, while
	// nothing writes quotas yet, so an entry there for this scope is the staler.
	if q, ok := legacySessionQuota(w.Pct, w.History); ok {
		byScope[SessionClaudeCode] = q
	}
	for _, q := range byScope {
		s.Quotas = append(s.Quotas, q)
	}
	sort.Slice(s.Quotas, func(i, j int) bool { return s.Quotas[i].Scope < s.Quotas[j].Scope })
	return s
}

// parseQuota fails closed: a fraction above 1 (often a percent written by
// mistake) clamps to a full context, with a warning, rather than reading as calm.
func parseQuota(scope string, raw json.RawMessage) (Quota, bool) {
	var e struct {
		ContextFrac *float64 `json:"context_frac"`
		History     any      `json:"history"`
	}
	if json.Unmarshal(raw, &e) != nil || e.ContextFrac == nil || *e.ContextFrac < 0 {
		return Quota{}, false
	}
	frac := *e.ContextFrac
	if frac > 1 {
		if _, seen := warnedOverFull.LoadOrStore(scope, true); !seen {
			log.Printf("⚠ quota %s has context_frac %v, above 1; treating it as full", scope, frac)
		}
		frac = 1
	}
	return Quota{Scope: scope, ContextFrac: frac, History: parseHistory(e.History)}, true
}

// warnedOverFull holds the scopes already warned about: state.json is read on
// every dispatch, and the warning needs saying once per process, not per read.
var warnedOverFull sync.Map

// legacySessionQuota derives SessionClaudeCode from claude_pct and its history
// on every read, so the next `jq` write needs no migration step.
func legacySessionQuota(pct, history any) (Quota, bool) {
	level, ok := pct.(float64)
	if !ok {
		return Quota{}, false
	}
	return Quota{
		Scope:       SessionClaudeCode,
		ContextFrac: float64(roundPct(level)) / 100,
		History:     parseHistory(history),
	}, true
}

// Governing is the most urgent quota (highest Mode, ties by Scope) and its
// mode, ok=false when there are none.
func Governing(qs []Quota) (q Quota, mode Mode, ok bool) {
	for _, c := range qs {
		m := c.Mode()
		if !ok || m > mode || (m == mode && c.Scope < q.Scope) {
			q, mode, ok = c, m, true
		}
	}
	return q, mode, ok
}

// MirrorLegacyHistory appends the orchestrator's level to its bounded history,
// so the rate-aware governor and the frozen legacy readers see the trajectory.
func MirrorLegacyHistory(state map[string]any) {
	pct, ok := state[legacyPctKey].(float64)
	if !ok {
		return
	}
	if level := roundPct(pct); level > 0 {
		state[legacyHistoryKey] = AppendPctHistory(
			parseHistory(state[legacyHistoryKey]), level, MaxPctHistory)
	}
}

// parseHistory takes a JSON-decoded series. One non-numeric point makes the
// whole series unknown: a null read as 0 would fake a burst of burn.
func parseHistory(v any) []int {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]int, 0, len(raw))
	for _, e := range raw {
		f, ok := e.(float64)
		if !ok {
			return nil
		}
		out = append(out, roundPct(f))
	}
	return out
}

// roundPct is the one rounding from a percentage to a level, so the level and
// its newest history point never disagree.
func roundPct(v float64) int {
	return min(max(int(math.Round(v)), 0), 100)
}
