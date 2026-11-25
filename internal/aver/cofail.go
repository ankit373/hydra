// SPDX-License-Identifier: MIT

package aver

import (
	"sort"

	"github.com/ankit373/hydra/internal/stats"
)

// MinSharedWrongPair bounds which pairs can be tested; below it a pair is linked.
const MinSharedWrongPair = 20

// Group is checks of one (kind, head) whose false passes may go together, so
// only Rep, the one with the best e on a pass, counts.
type Group struct {
	Kind   string   `json:"kind"`
	Head   string   `json:"head"`
	Checks []string `json:"checks"`
	Rep    string   `json:"rep"`
}

// Groups is the gate's result. Linked counts pairs joined on evidence, Untestable
// pairs joined for want of MinSharedWrongPair shared wrong answers.
type Groups struct {
	Groups     []Group `json:"groups"`
	Linked     int     `json:"linked"`
	Untestable int     `json:"untestable"`
}

// Gate tests each pair of checks per (kind, head) on the wrong answers both ran on:
// a pair whose Clopper-Pearson bound on passing together, at t.Delta, exceeds
// q̄_a q̄_b is linked. Linked checks form groups by union-find over the pairs.
func Gate(answers []Answer, t Table) Groups {
	type pairKey struct{ kind, head, a, b string }
	type tally struct{ shared, both int }
	pairs := map[pairKey]*tally{}
	for _, a := range answers {
		if a.Correct {
			continue
		}
		names := make([]string, 0, len(a.Verdicts))
		for c := range a.Verdicts {
			names = append(names, c)
		}
		sort.Strings(names)
		for i, x := range names {
			for _, y := range names[i+1:] {
				k := pairKey{a.Kind, a.Head, x, y}
				p := pairs[k]
				if p == nil {
					p = &tally{}
					pairs[k] = p
				}
				p.shared++
				if a.Verdicts[x] && a.Verdicts[y] {
					p.both++
				}
			}
		}
	}

	idx := t.Index()
	parent := map[[3]string][3]string{}
	var find func(x [3]string) [3]string
	find = func(x [3]string) [3]string {
		p, ok := parent[x]
		if !ok || p == x {
			parent[x] = x
			return x
		}
		root := find(p)
		parent[x] = root
		return root
	}
	var g Groups
	for k, p := range pairs {
		a, b := [3]string{k.kind, k.a, k.head}, [3]string{k.kind, k.b, k.head}
		find(a)
		find(b)
		if p.shared < MinSharedWrongPair {
			g.Untestable++
			parent[find(a)] = find(b)
			continue
		}
		// Cannot fail: shared > 0 and both <= shared.
		upper, _ := stats.ClopperPearsonUpper(p.both, p.shared, t.Delta)
		if upper > idx[a].QBar*idx[b].QBar {
			g.Linked++
			parent[find(a)] = find(b)
		}
	}

	members := map[[3]string][]Cell{}
	for k := range parent {
		root := find(k)
		members[root] = append(members[root], idx[k])
	}
	for _, cs := range members {
		if len(cs) < 2 {
			continue
		}
		sort.Slice(cs, func(i, j int) bool { return cs[i].Check < cs[j].Check })
		grp := Group{Kind: cs[0].Kind, Head: cs[0].Head, Rep: cs[0].Check}
		best := cs[0].EPass()
		for _, c := range cs {
			grp.Checks = append(grp.Checks, c.Check)
			if e := c.EPass(); e > best {
				grp.Rep, best = c.Check, e
			}
		}
		g.Groups = append(g.Groups, grp)
	}
	sort.Slice(g.Groups, func(i, j int) bool {
		a, b := g.Groups[i], g.Groups[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Head != b.Head {
			return a.Head < b.Head
		}
		return a.Rep < b.Rep
	})
	return g
}

// Apply skips every grouped check but its group's Rep, so each group counts once
// and groups multiply with each other.
func (g Groups) Apply(t Table) Table {
	rep := map[[3]string]string{}
	for _, grp := range g.Groups {
		for _, c := range grp.Checks {
			rep[[3]string{grp.Kind, c, grp.Head}] = grp.Rep
		}
	}
	out := Table{Delta: t.Delta, Cells: make([]Cell, len(t.Cells))}
	for i, c := range t.Cells {
		if r, ok := rep[[3]string{c.Kind, c.Check, c.Head}]; ok && r != c.Check {
			c.Skip, c.LinkedTo = true, r
		}
		out.Cells[i] = c
	}
	return out
}
