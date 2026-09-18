// SPDX-License-Identifier: MIT

package policy

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// A rule whose condition names a field the evaluator does not know can never
// match. Before #848 it matched *everything* instead, which is worse, but
// inert is still not the same as absent and an operator reading the file
// cannot tell one from the other.

// DeadCondition is one `when` key that can never be satisfied.
type DeadCondition struct {
	Rule   string `json:"rule"`
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

func (d DeadCondition) String() string {
	return fmt.Sprintf("%s: %s (%s)", d.Rule, d.Key, d.Reason)
}

// specFieldNames is the set of field names a condition may name, read off the
// json tags rather than written down, so adding a field to Spec cannot leave
// this claiming the field is unknown.
func specFieldNames() map[string]bool {
	t := reflect.TypeOf(Spec{})
	names := make(map[string]bool, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		if tag := t.Field(i).Tag.Get("json"); tag != "" {
			names[strings.Split(tag, ",")[0]] = true
		}
	}
	return names
}

// conditionField strips the operator suffix, the same way matchCondition does.
// Kept beside it deliberately: a key that parses differently here than there
// would report a live rule dead, or miss a dead one.
func conditionField(key string) string {
	for _, suffix := range condSuffixes {
		if strings.HasSuffix(key, suffix) {
			return strings.TrimSuffix(key, suffix)
		}
	}
	return key
}

// DeadConditions names every `when` key whose field the evaluator does not
// recognise, with the rule it sits in. Sorted, so the report is stable.
//
// Only provable deadness is reported. A rule that has simply not come up yet
// is a different thing entirely and is not guessed at here.
func (e *FilePolicyEngine) DeadConditions() []DeadCondition {
	known := specFieldNames()
	var out []DeadCondition
	for _, rule := range e.pf.Rules {
		for key := range rule.When {
			if key == "always" {
				continue
			}
			if field := conditionField(key); !known[field] {
				out = append(out, DeadCondition{
					Rule: rule.Name, Key: key,
					Reason: fmt.Sprintf("no task field named %q", field),
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rule != out[j].Rule {
			return out[i].Rule < out[j].Rule
		}
		return out[i].Key < out[j].Key
	})
	return out
}
