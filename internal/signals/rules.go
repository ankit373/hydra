// SPDX-License-Identifier: MIT

package signals

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ankit373/hydra/registry"
)

// FileName is the registry file rules are read from.
const FileName = "signals.yaml"

// ActionType is what a matching rule does.
type ActionType string

const (
	// ActionFallthrough is today's routing, unchanged. The default, and what a
	// machine with no rules file gets.
	ActionFallthrough ActionType = "fallthrough"
	// ActionRoute pins tier, enum or local-only.
	ActionRoute ActionType = "route"
	// ActionRequireConfidence raises the confidence bar for this dispatch.
	ActionRequireConfidence ActionType = "require_confidence"
	// ActionBlock refuses the dispatch outright.
	ActionBlock ActionType = "block"
)

// Action is what happens when a rule matches.
type Action struct {
	Type      ActionType `yaml:"type"`
	Tier      string     `yaml:"tier,omitempty"`
	Enum      string     `yaml:"enum,omitempty"`
	LocalOnly bool       `yaml:"local_only,omitempty"`
	Value     float64    `yaml:"value,omitempty"`
	Reason    string     `yaml:"reason,omitempty"`
}

// Rule is one priority-ordered condition and its action.
type Rule struct {
	Name     string `yaml:"name"`
	Priority int    `yaml:"priority"`
	When     string `yaml:"when,omitempty"`
	Action   Action `yaml:"action"`

	expr *node
}

// file is signals.yaml's shape.
type file struct {
	Version  int       `yaml:"version"`
	Keywords []Keyword `yaml:"keywords"`
	Rules    []Rule    `yaml:"rules"`
}

// Engine evaluates rules against signals. Read-only after Parse.
type Engine struct {
	keywords []Keyword
	rules    []Rule
	schema   Schema
}

// Decision is what evaluation concluded.
type Decision struct {
	// Rule is the rule that matched, empty when none did.
	Rule   string
	Action Action
	// Signals is every value the rules were evaluated against, so --dry-run can
	// show why a rule fired rather than only that it did.
	Signals Set
}

// Fired reports whether a rule changed anything. A fallthrough match is a rule
// firing and choosing today's behaviour, which is worth showing and is not the
// same as no rule matching at all.
func (d Decision) Fired() bool { return d.Rule != "" && d.Action.Type != ActionFallthrough }

// ActionValidator is an extra check on an action's values that this package
// cannot make itself, e.g. whether a tier name resolves. Returning an error
// fails the load, so a typo is caught there rather than at dispatch.
type ActionValidator func(Action) error

// Load reads signals.yaml through the registry, so an on-disk copy at
// $HYDRA_HOME/registry/signals.yaml wins and the embedded one ships in the
// binary. An on-disk file replaces the embedded rules wholesale rather than
// merging with them: a half-applied rule set is worse than either.
func Load(home string, extra ActionValidator) (*Engine, error) {
	raw, err := registry.Read(home, FileName)
	if err != nil {
		// No rules at all is a valid state and the one every install starts in.
		return &Engine{schema: SchemaFor(nil)}, nil
	}
	return Parse(raw, extra)
}

// Parse builds an engine, refusing anything it cannot fully check.
func Parse(raw []byte, extra ActionValidator) (*Engine, error) {
	var f file
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	if f.Version != 0 && f.Version != 1 {
		return nil, fmt.Errorf("%s: unsupported version %d", FileName, f.Version)
	}

	seenKeyword := map[string]string{}
	for _, k := range f.Keywords {
		if strings.TrimSpace(k.Name) == "" {
			return nil, fmt.Errorf("%s: a keyword set has no name", FileName)
		}
		norm := Normalize(k.Name)
		if norm == "" {
			return nil, fmt.Errorf("%s: keyword %q normalises to nothing", FileName, k.Name)
		}
		if prev, dup := seenKeyword[norm]; dup {
			return nil, fmt.Errorf("%s: keywords %q and %q both name the signal %s",
				FileName, prev, k.Name, KeywordSignal(k.Name))
		}
		seenKeyword[norm] = k.Name
		if len(k.Any) == 0 {
			return nil, fmt.Errorf("%s: keyword %q lists no phrases, so it can never match", FileName, k.Name)
		}
	}

	e := &Engine{keywords: f.Keywords, schema: SchemaFor(f.Keywords)}

	seenRule := map[string]bool{}
	for i := range f.Rules {
		r := f.Rules[i]
		if strings.TrimSpace(r.Name) == "" {
			return nil, fmt.Errorf("%s: rule %d has no name", FileName, i+1)
		}
		if seenRule[r.Name] {
			return nil, fmt.Errorf("%s: two rules are named %q", FileName, r.Name)
		}
		seenRule[r.Name] = true

		if err := validateAction(r.Action, extra); err != nil {
			return nil, fmt.Errorf("%s: rule %q: %w", FileName, r.Name, err)
		}

		if strings.TrimSpace(r.When) != "" {
			n, err := parseExpr(r.When)
			if err != nil {
				return nil, fmt.Errorf("%s: rule %q: %w", FileName, r.Name, err)
			}
			if err := check(n, e.schema); err != nil {
				return nil, fmt.Errorf("%s: rule %q: %w", FileName, r.Name, err)
			}
			if n.kind != KindBool {
				return nil, fmt.Errorf("%s: rule %q: when must be a condition, got %s",
					FileName, r.Name, n.kind)
			}
			r.expr = n
		}
		e.rules = append(e.rules, r)
	}

	// Total and deterministic: priority descending, then name. Never map order,
	// and never a comparator that leaves a pair undecided (#765).
	sort.Slice(e.rules, func(i, j int) bool {
		if e.rules[i].Priority != e.rules[j].Priority {
			return e.rules[i].Priority > e.rules[j].Priority
		}
		return e.rules[i].Name < e.rules[j].Name
	})
	return e, nil
}

func validateAction(a Action, extra ActionValidator) error {
	switch a.Type {
	case ActionFallthrough:
	case ActionRoute:
		if a.Tier == "" && a.Enum == "" && !a.LocalOnly {
			return fmt.Errorf("a route action that pins nothing does nothing")
		}
	case ActionRequireConfidence:
		if a.Value <= 0 || a.Value >= 1 {
			return fmt.Errorf("require_confidence value must be between 0 and 1 exclusive, got %v", a.Value)
		}
	case ActionBlock:
		if strings.TrimSpace(a.Reason) == "" {
			return fmt.Errorf("a block action must say why")
		}
	case "":
		return fmt.Errorf("no action type")
	default:
		return fmt.Errorf("unknown action type %q", a.Type)
	}
	if extra != nil {
		return extra(a)
	}
	return nil
}

// Evaluate collects the signals once and returns the first matching rule.
//
// Highest priority first, first match wins. A rule whose expression fails at
// evaluation is skipped rather than fatal: the rules advise routing, and a
// dispatch must not die because one of them could not be computed.
func (e *Engine) Evaluate(in Input) Decision {
	// A nil engine is the normal state of a machine with no rules file, so the
	// nil check comes before the field read rather than after it.
	var keywords []Keyword
	if e != nil {
		keywords = e.keywords
	}
	vals := Collect(in, keywords)
	d := Decision{Signals: vals, Action: Action{Type: ActionFallthrough}}
	if e == nil {
		return d
	}
	for _, r := range e.rules {
		if r.expr == nil {
			// No condition is an unconditional rule, which is how the default
			// is written.
			d.Rule, d.Action = r.Name, r.Action
			return d
		}
		ok, err := evalBool(r.expr, vals)
		if err != nil || !ok {
			continue
		}
		d.Rule, d.Action = r.Name, r.Action
		return d
	}
	return d
}

// Rules lists the loaded rules in evaluation order.
func (e *Engine) Rules() []Rule {
	if e == nil {
		return nil
	}
	return append([]Rule(nil), e.rules...)
}

// Signals lists every signal name a rule may reference, sorted.
func (e *Engine) Signals() []string {
	if e == nil {
		return nil
	}
	out := make([]string, 0, len(e.schema))
	for name := range e.schema {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Inert lists rules that can never fire, for `hyctl security`. A rule after an
// unconditional one is unreachable however true its own condition is, which is
// the one case a load-time check cannot call an error: the file is valid, the
// rule is just dead.
func (e *Engine) Inert() []string {
	if e == nil {
		return nil
	}
	var out []string
	unconditional := ""
	for _, r := range e.rules {
		if unconditional != "" {
			out = append(out, fmt.Sprintf("%s (unreachable: %q matches everything before it)", r.Name, unconditional))
			continue
		}
		if r.expr == nil {
			unconditional = r.Name
		}
	}
	return out
}

// Referenced lists the signals a rule names, sorted, for reporting.
func (r Rule) Referenced() []string {
	if r.expr == nil {
		return nil
	}
	set := map[string]struct{}{}
	identifiers(r.expr, set)
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
