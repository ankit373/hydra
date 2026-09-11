// SPDX-License-Identifier: MIT

package cost

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"testing"
)

// Two packages append to cost.jsonl and neither knows what the other writes.
// That is how span_id, added in #719 so spend joins the span that spent it,
// reached only one of them: the swarm and SPRT rows, the ones where several
// attempts share a task id and the join is the entire point, carried no span
// at all (#794).
//
// Keys are compared rather than behaviour because a missing key is exactly
// what neither writer can observe about the other. A key genuinely belonging
// to one writer goes in exemptions, with the reason, so an omission has to be
// argued rather than merely happen.
var costWriterExemptions = map[string]string{
	// A fan-out reports which mode ran and whether the attempt won it. A plain
	// dispatch has no fan-out to describe.
	"swarm_mode":   "swarm only: there is no mode on a single dispatch",
	"swarm_winner": "swarm only: nothing to win",

	// --enum reaches dispatch.Options and stops there; swarm.Options has no
	// Enum field, so a swarm is never routed by an enum even when one was
	// passed. Writing it here would record a routing key that did not route.
	"enum": "dispatch only: an enum does not reach the swarm path at all",

	// The dispatch log carries the preview on its own entry rather than on the
	// cost row; swarm has one writer for both.
	"prompt_preview": "swarm only: swarm has no separate dispatch-log entry",
}

func TestCostWriters_NeitherOmitsWhatTheOtherRecords(t *testing.T) {
	dispatchKeys := costEntryKeys(t, "../dispatch/dispatch.go")
	swarmKeys := costEntryKeys(t, "../swarm/cost.go")

	if len(dispatchKeys) == 0 || len(swarmKeys) == 0 {
		t.Fatalf("found %d dispatch keys and %d swarm keys; the entry is no longer "+
			"a map literal carrying cost_source and this guard reads nothing",
			len(dispatchKeys), len(swarmKeys))
	}

	for _, c := range []struct {
		from, to   string
		have, want map[string]bool
	}{
		{"internal/dispatch", "internal/swarm", dispatchKeys, swarmKeys},
		{"internal/swarm", "internal/dispatch", swarmKeys, dispatchKeys},
	} {
		for _, k := range sortedKeys(c.have) {
			if c.want[k] {
				continue
			}
			if why, ok := costWriterExemptions[k]; ok {
				t.Logf("%q is written by %s alone: %s", k, c.from, why)
				continue
			}
			t.Errorf("%s writes %q to cost.jsonl and %s does not. Either write it "+
				"there too, or record in costWriterExemptions why that row cannot "+
				"carry it.", c.from, k, c.to)
		}
	}
}

// costEntryKeys returns the keys of the map literal a file writes to
// cost.jsonl, identified by cost_source. est_cost_usd alone is not enough: a
// runlog event's Meta carries it when a head is refused for exceeding a cost
// ceiling, and this read that as a cost row.
func costEntryKeys(t *testing.T, path string) map[string]bool {
	t.Helper()

	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	out := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		keys := map[string]bool{}
		for _, el := range lit.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if s, ok := kv.Key.(*ast.BasicLit); ok && s.Kind == token.STRING {
				keys[mustUnquote(t, s.Value)] = true
			}
		}
		if !keys["cost_source"] {
			return true
		}
		for k := range keys {
			out[k] = true
		}
		return true
	})
	return out
}

func mustUnquote(t *testing.T, s string) string {
	t.Helper()
	if len(s) < 2 {
		t.Fatalf("not a quoted string: %s", s)
	}
	return s[1 : len(s)-1]
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
