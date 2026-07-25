// SPDX-License-Identifier: MIT

// Package ledger is Hydra's local MCP accountability ledger: an append-only
// record of what every agent was allowed to touch and did. A policy gate decides
// allow/deny for each tool/resource access and writes the decision to the ledger,
// so there is always a local, queryable trail — the accountability half of
// multi-model safety.
package ledger

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Action is the kind of access an agent attempts against a resource.
type Action string

const (
	Read    Action = "read"
	Write   Action = "write"
	Exec    Action = "exec"
	Network Action = "network"
)

// Decision is the gate's verdict.
type Decision string

const (
	Allow Decision = "allow"
	Deny  Decision = "deny"
)

// Event is one recorded access attempt.
type Event struct {
	TS       string   `json:"ts"`
	Agent    string   `json:"agent"`
	Tool     string   `json:"tool"`
	Resource string   `json:"resource"`
	Action   Action   `json:"action"`
	Decision Decision `json:"decision"`
	Reason   string   `json:"reason,omitempty"`
}

// DefaultPath is where the ledger lives (~/.hydra/mcp_ledger.jsonl).
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".hydra", "mcp_ledger.jsonl")
}

// Record appends one event, stamping TS if blank.
func Record(path string, e Event) error {
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(time.RFC3339)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(f, string(raw))
	return err
}

// Load reads all events; a missing ledger yields no events.
func Load(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var events []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		events = append(events, e)
	}
	return events, sc.Err()
}

// Rule is one allow/deny rule. Empty fields (and "*") match anything; Resource
// is matched as a glob (path.Match semantics via filepath.Match).
type Rule struct {
	Agent    string   `json:"agent,omitempty"`
	Tool     string   `json:"tool,omitempty"`
	Resource string   `json:"resource,omitempty"`
	Action   Action   `json:"action,omitempty"`
	Decision Decision `json:"decision"`
}

// Policy is an ordered rule set with a default decision. First matching rule wins.
type Policy struct {
	Rules   []Rule   `json:"rules"`
	Default Decision `json:"default"`
}

// Decide evaluates an access against the policy, returning the decision and a
// human-readable reason. A zero Default is treated as Allow (Hydra records
// everything but blocks nothing unless a rule says so).
func (p Policy) Decide(agent, tool, resource string, action Action) (Decision, string) {
	for i, r := range p.Rules {
		if r.matches(agent, tool, resource, action) {
			return r.Decision, fmt.Sprintf("rule %d (%s %s/%s)", i, r.Decision, ruleOr(r.Tool), ruleOr(r.Resource))
		}
	}
	def := p.Default
	if def == "" {
		def = Allow
	}
	return def, "default"
}

func (r Rule) matches(agent, tool, resource string, action Action) bool {
	return fieldMatch(r.Agent, agent) &&
		fieldMatch(r.Tool, tool) &&
		globMatch(r.Resource, resource) &&
		(r.Action == "" || r.Action == action)
}

// fieldMatch: empty or "*" matches anything; otherwise exact.
func fieldMatch(pattern, v string) bool {
	return pattern == "" || pattern == "*" || pattern == v
}

// globMatch: empty or "*" matches anything; otherwise filepath.Match glob. A
// malformed pattern falls back to exact comparison rather than erroring.
func globMatch(pattern, v string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	ok, err := filepath.Match(pattern, v)
	if err != nil {
		return pattern == v
	}
	return ok
}

func ruleOr(s string) string {
	if s == "" {
		return "*"
	}
	return s
}

// DefaultPolicyPath is where the access policy lives (~/.hydra/mcp_policy.json).
func DefaultPolicyPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".hydra", "mcp_policy.json")
}

// LoadPolicy reads a policy file. A missing file yields a default-allow policy
// (Hydra records everything but blocks nothing until rules are defined).
func LoadPolicy(path string) (Policy, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Policy{Default: Allow}, nil
		}
		return Policy{}, err
	}
	var p Policy
	if err := json.Unmarshal(raw, &p); err != nil {
		return Policy{}, err
	}
	if p.Default == "" {
		p.Default = Allow
	}
	return p, nil
}

// Check evaluates the policy AND records the resulting event to the ledger —
// the accountability gate. It returns the decision.
func Check(path string, p Policy, agent, tool, resource string, action Action) (Decision, error) {
	decision, reason := p.Decide(agent, tool, resource, action)
	err := Record(path, Event{
		Agent: agent, Tool: tool, Resource: resource,
		Action: action, Decision: decision, Reason: reason,
	})
	return decision, err
}

// Summary is the aggregate accountability report.
type Summary struct {
	Total   int            `json:"total"`
	Allowed int            `json:"allowed"`
	Denied  int            `json:"denied"`
	ByAgent map[string]int `json:"by_agent"`
	ByTool  map[string]int `json:"by_tool"`
}

// Summarize aggregates events for `hydra mcp report`.
func Summarize(events []Event) Summary {
	s := Summary{ByAgent: map[string]int{}, ByTool: map[string]int{}}
	for _, e := range events {
		s.Total++
		switch e.Decision {
		case Allow:
			s.Allowed++
		case Deny:
			s.Denied++
		}
		s.ByAgent[e.Agent]++
		s.ByTool[e.Tool]++
	}
	return s
}

// Filter returns events matching a non-empty agent and/or only denials.
func Filter(events []Event, agent string, deniedOnly bool) []Event {
	var out []Event
	for _, e := range events {
		if agent != "" && e.Agent != agent {
			continue
		}
		if deniedOnly && e.Decision != Deny {
			continue
		}
		out = append(out, e)
	}
	return out
}

// SortedCounts returns key/count pairs sorted by count descending, for stable
// report rendering.
func SortedCounts(m map[string]int) []struct {
	Key   string
	Count int
} {
	out := make([]struct {
		Key   string
		Count int
	}, 0, len(m))
	for k, v := range m {
		out = append(out, struct {
			Key   string
			Count int
		}{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Key < out[j].Key
	})
	return out
}
