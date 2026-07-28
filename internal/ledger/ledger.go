// SPDX-License-Identifier: MIT

// Package ledger is Hydra's local MCP accountability ledger: an append-only
// record of what every agent was allowed to touch and did. A policy gate decides
// allow/deny for each tool/resource access and writes the decision to the ledger,
// so there is always a local, queryable trail — the accountability half of
// multi-model safety.
package ledger

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/policy"
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

	// ParametersHash binds this decision to the exact parameters it was made
	// for (HashParams) — tamper-evidence between an access decision and the
	// parameters actually used at execution time.
	ParametersHash string `json:"parameters_hash,omitempty"`
	// Classification is the data-sensitivity tag this access was evaluated
	// under (e.g. "pii"). Empty means unclassified.
	Classification string `json:"classification,omitempty"`
}

// HashParams returns a SHA256 hex hash of params, for tamper-evident binding
// between an access decision and the parameters actually used at execution
// time. Go's json.Marshal sorts map[string]any keys, so this is canonical
// regardless of map iteration order.
func HashParams(params map[string]any) (string, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// VerifyParams reports whether params match a previously recorded hash — the
// check to run at execution time to detect tampering between an approval and
// its use (e.g. once decision and execution can happen on different
// machines/agents).
func VerifyParams(params map[string]any, hash string) (bool, error) {
	got, err := HashParams(params)
	if err != nil {
		return false, err
	}
	return got == hash, nil
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
	Agent          string   `json:"agent,omitempty"`
	Tool           string   `json:"tool,omitempty"`
	Resource       string   `json:"resource,omitempty"`
	Action         Action   `json:"action,omitempty"`
	Classification string   `json:"classification,omitempty"`
	Decision       Decision `json:"decision"`
}

// Policy is an ordered rule set with a default decision. First matching rule wins.
type Policy struct {
	Rules   []Rule   `json:"rules"`
	Default Decision `json:"default"`
}

// Decide evaluates an access against the policy, returning the decision and a
// human-readable reason. A zero Default is treated as Allow (Hydra records
// everything but blocks nothing unless a rule says so). classification is the
// data-sensitivity tag for this access ("" if unclassified or not applicable).
func (p Policy) Decide(agent, tool, resource string, action Action, classification string) (Decision, string) {
	for i, r := range p.Rules {
		if r.matches(agent, tool, resource, action, classification) {
			return r.Decision, fmt.Sprintf("rule %d (%s %s/%s)", i, r.Decision, ruleOr(r.Tool), ruleOr(r.Resource))
		}
	}
	def := p.Default
	if def == "" {
		def = Allow
	}
	return def, "default"
}

func (r Rule) matches(agent, tool, resource string, action Action, classification string) bool {
	return fieldMatch(r.Agent, agent) &&
		fieldMatch(r.Tool, tool) &&
		globMatch(r.Resource, resource) &&
		(r.Action == "" || r.Action == action) &&
		fieldMatch(r.Classification, classification)
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

// CheckRequest describes one access-check request: what's being accessed, by
// whom, with what parameters, and (optionally) its data classification.
type CheckRequest struct {
	Agent    string
	Tool     string
	Resource string
	Action   Action

	// Params is hashed (HashParams) and recorded as Event.ParametersHash,
	// binding this decision to the exact parameters it was made for.
	Params map[string]any

	// Classification is the data-sensitivity tag (e.g. "pii"). If empty and
	// Content is non-empty, it is derived via policy.ContainsPII(Content).
	Classification string
	Content        string
}

// Check evaluates the policy AND records the resulting event to the ledger —
// the accountability gate. It returns the decision.
func Check(path string, p Policy, req CheckRequest) (Decision, error) {
	classification := req.Classification
	if classification == "" && req.Content != "" && policy.ContainsPII(policy.Request{Prompt: req.Content}) {
		classification = "pii"
	}

	var hash string
	if len(req.Params) > 0 {
		h, err := HashParams(req.Params)
		if err != nil {
			return "", err
		}
		hash = h
	}

	decision, reason := p.Decide(req.Agent, req.Tool, req.Resource, req.Action, classification)
	err := Record(path, Event{
		Agent: req.Agent, Tool: req.Tool, Resource: req.Resource,
		Action: req.Action, Decision: decision, Reason: reason,
		ParametersHash: hash, Classification: classification,
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
