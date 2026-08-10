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

	"github.com/ankit373/hydra/internal/glob"
	"sort"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/config"
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

// ParseAction normalizes and validates an action string. Matching is exact, so
// an unrecognized or differently-cased value would silently miss every
// action-scoped rule and fall through to the default — hence a hard error
// rather than a best-effort coercion.
func ParseAction(s string) (Action, error) {
	switch a := Action(strings.ToLower(strings.TrimSpace(s))); a {
	case Read, Write, Exec, Network:
		return a, nil
	case "":
		return "", fmt.Errorf("action is required (read|write|exec|network)")
	default:
		return "", fmt.Errorf("unknown action %q (want read|write|exec|network)", s)
	}
}

// ParseDecision normalizes and validates a decision string. An unrecognized
// value is rejected because callers gate on Deny exactly: a decision of "DENY"
// or "block" would print like a denial while comparing unequal to Deny.
func ParseDecision(s string) (Decision, error) {
	switch d := Decision(strings.ToLower(strings.TrimSpace(s))); d {
	case Allow, Deny:
		return d, nil
	case "":
		return "", fmt.Errorf("decision is required (allow|deny)")
	default:
		return "", fmt.Errorf("unknown decision %q (want allow|deny)", s)
	}
}

// NormalizeClassification lowercases a data-sensitivity tag so a caller that
// declares "PII" cannot slip past a rule written as "pii".
func NormalizeClassification(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

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

	// Config is the deployment-identity breadcrumb (config.Breadcrumb) in
	// effect when this event was recorded, ties the event to the exact
	// routing rules that were live.
	Config string `json:"config,omitempty"`
}

// HashParams returns a SHA256 hex hash of params, for tamper-evident binding
// between an access decision and the parameters actually used at execution
// time. Go's json.Marshal sorts map[string]any keys, so this is canonical
// regardless of map iteration order.
//
// Scope: the hash covers the parameters ONLY — not the tool, resource, or
// action. Two approvals with identical parameters for different resources
// therefore share a hash, so a verifier must match the tool/resource itself
// (see LatestBound) rather than treating a hash match as proof of which
// operation was approved.
//
// Decode JSON parameters with DecodeParams, not a plain json.Unmarshal: that
// keeps numbers as json.Number so their exact literal is hashed. Decoding into
// a bare any turns every number into a float64, which silently collapses
// integers above 2^53 — 1000000000000000001 and ...002 would share a hash.
func HashParams(params map[string]any) (string, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// DecodeParams parses a JSON object of invocation parameters, preserving each
// number's exact literal (json.Number) so HashParams cannot collide two
// different large integers through float64 rounding. A JSON `null` decodes to
// a nil map, meaning "no parameters supplied".
func DecodeParams(raw string) (map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var params map[string]any
	if err := dec.Decode(&params); err != nil {
		return nil, err
	}
	return params, nil
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

// LatestBound returns the most recent *allowed* event for a tool/resource that
// carries a parameters hash — the approval that execution-time params should be
// verified against. An empty tool or resource matches any.
//
// Only Allow events qualify: a denied attempt is recorded with the parameters
// it was refused for, and treating that as an approval would let a verifier
// confirm exactly the parameters the gate just rejected.
func LatestBound(events []Event, tool, resource string) (Event, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.ParametersHash == "" || e.Decision != Allow {
			continue
		}
		if fieldMatch(tool, e.Tool) && fieldMatch(resource, e.Resource) {
			return e, true
		}
	}
	return Event{}, false
}

// DefaultPath is where the ledger lives (~/.hydra/mcp_ledger.jsonl).
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".hydra", "mcp_ledger.jsonl")
}

// Record appends one event, stamping TS and Config (best-effort) if blank.
func Record(path string, e Event) error {
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(time.RFC3339)
	}
	if e.Config == "" {
		if bc, err := config.Breadcrumb(); err == nil {
			e.Config = bc
		}
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

// Load reads all events; a missing ledger yields no events. Unparseable lines
// are skipped — use LoadCounted when the caller needs to report them.
func Load(path string) ([]Event, error) {
	events, _, err := LoadCounted(path)
	return events, err
}

// LoadCounted is Load plus the number of unparseable lines skipped. An
// append-only accountability ledger that silently discards records is a
// contradiction, and a truncated tail (a crash mid-write) would otherwise
// make verification fall back to an older approval with no indication — so
// callers should surface a non-zero count.
func LoadCounted(path string) ([]Event, int, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	defer f.Close()

	var events []Event
	var skipped int
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			skipped++
			continue
		}
		events = append(events, e)
	}
	return events, skipped, sc.Err()
}

// Rule is one allow/deny rule. Empty fields (and "*") match anything; Resource
// is matched as a glob (path.Match semantics via filepath.Match).
//
// Note that filepath.Match's `*` does NOT cross a path separator, and there is
// no recursive `**`: a rule for "/etc/*" matches "/etc/passwd" but not
// "/etc/ssh/sshd_config". Deny rules intended to cover a subtree must enumerate
// each depth (or match on a prefix-free resource naming scheme).
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
// globMatch delegates to the shared dialect. It used filepath.Match, which has
// no "**" and whose separator is "\" on Windows — so "/repo/*" matched one
// level on Unix and arbitrarily deep on Windows, and a "**/secrets/**" rule
// copied from workspace.yaml matched nothing at all (#310).
func globMatch(pattern, v string) bool { return glob.Match(pattern, v) }

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
	if err := p.validate(); err != nil {
		return Policy{}, fmt.Errorf("policy %s: %w", path, err)
	}
	return p, nil
}

// validate normalizes and checks every rule. A policy is rejected rather than
// partially honored: a rule whose decision or action does not parse can never
// match (or can never deny), so loading it would silently weaken the gate —
// a default-deny posture written as "DENY" would void entirely.
func (p *Policy) validate() error {
	d, err := ParseDecision(string(p.Default))
	if err != nil {
		return fmt.Errorf("default: %w", err)
	}
	p.Default = d

	for i := range p.Rules {
		r := &p.Rules[i]
		d, err := ParseDecision(string(r.Decision))
		if err != nil {
			return fmt.Errorf("rule %d: %w", i, err)
		}
		r.Decision = d
		if r.Action != "" {
			a, err := ParseAction(string(r.Action))
			if err != nil {
				return fmt.Errorf("rule %d: %w", i, err)
			}
			r.Action = a
		}
		// A malformed glob can never match, so globMatch falls back to exact
		// comparison — meaning a typo'd deny rule silently permits everything
		// it was written to block. Reject it at load instead.
		if _, err := filepath.Match(r.Resource, ""); err != nil {
			return fmt.Errorf("rule %d: invalid resource pattern %q: %w", i, r.Resource, err)
		}
		r.Classification = NormalizeClassification(r.Classification)
	}
	return nil
}

// CheckRequest describes one access-check request: what's being accessed, by
// whom, with what parameters, and (optionally) its data classification.
type CheckRequest struct {
	Agent    string
	Tool     string
	Resource string
	Action   Action

	// Params is hashed (HashParams) and recorded as Event.ParametersHash,
	// binding this decision to the exact parameters it was made for. A non-nil
	// but empty map still binds (a no-argument invocation is a real, verifiable
	// operation); only nil means "no parameters supplied".
	Params map[string]any

	// Classification is the data-sensitivity tag (e.g. "pii"). If empty and
	// Content is non-empty, it is derived via policy.ContainsPII(Content).
	Classification string
	Content        string
}

// Check evaluates the policy AND records the resulting event to the ledger —
// the accountability gate. It returns the decision.
//
// Check fails closed: if the request's parameters cannot be hashed, it returns
// Deny (and records that denial) rather than a zero Decision, so a caller that
// only tests `decision == Deny` can never be tricked into proceeding.
func Check(path string, p Policy, req CheckRequest) (Decision, error) {
	classification := NormalizeClassification(req.Classification)
	if classification == "" && req.Content != "" && policy.ContainsPII(policy.Request{Prompt: req.Content}) {
		classification = "pii"
	}

	var hash string
	if req.Params != nil {
		h, err := HashParams(req.Params)
		if err != nil {
			// Unhashable params cannot be bound to a decision, so the access is
			// denied — and the denial is itself recorded for accountability.
			_ = Record(path, Event{
				Agent: req.Agent, Tool: req.Tool, Resource: req.Resource,
				Action: req.Action, Decision: Deny, Classification: classification,
				Reason: "unhashable parameters: " + err.Error(),
			})
			return Deny, err
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

// CheckAndRecordDispatch is the automatic-dispatch-path counterpart to the
// manual `hyctl mcp check`: it loads the default policy, evaluates one head
// call as a tool/resource access (Action=Exec), and records the decision —
// so a real dispatch produces a ledger event without anyone invoking
// `hyctl mcp` by hand. Default (no rules configured) policy is Allow, so this
// only changes behavior for an install that has deliberately configured a
// deny rule.
func CheckAndRecordDispatch(agent, headID, resource, content string) (Decision, error) {
	pol, err := LoadPolicy(DefaultPolicyPath())
	if err != nil {
		return "", err
	}
	return Check(DefaultPath(), pol, CheckRequest{
		Agent: agent, Tool: headID, Resource: resource, Action: Exec, Content: content,
	})
}

// Summary is the aggregate accountability report.
type Summary struct {
	Total   int            `json:"total"`
	Allowed int            `json:"allowed"`
	Denied  int            `json:"denied"`
	ByAgent map[string]int `json:"by_agent"`
	ByTool  map[string]int `json:"by_tool"`
}

// Summarize aggregates events for `hyctl mcp report`.
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
