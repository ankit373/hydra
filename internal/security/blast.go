// SPDX-License-Identifier: MIT

package security

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/graph"
	"github.com/ankit373/hydra/internal/runlog"
)

// What did the agents actually touch, and how far does it reach?
//
// Every other section here reasons about access decisions. This one reasons
// about consequences: an agent editing a leaf file and an agent editing a file
// forty others depend on are the same ledger event and wildly different risks.
// Both halves already existed and were never joined — runlog records an `edit`
// event per file written, and internal/graph scores a file's transitive
// blast radius from graph.json.
//
// The honesty rule is inherited from internal/graph itself, which grew Empty()
// and Knows() precisely so a UI could not render the neutral 1.0 floor as an
// affirmative "this edit was safe" (#251). A file the graph does not index is
// reported as unknown, never as low-risk.

// maxBlastRuns caps how many recent runs are scanned. Run logs are small but
// unbounded in number; the newest runs are the ones an operator is reasoning
// about, and the cap is reported rather than applied silently.
const maxBlastRuns = 50

// EditedFile is one file an agent wrote, and how far a change to it reaches.
type EditedFile struct {
	File string `json:"file"`
	// Edits is how many edit events touched it across the scanned runs.
	Edits int `json:"edits"`
	// Radius and Dependents are only meaningful when Known.
	Radius     float64 `json:"radius,omitempty"`
	Dependents int     `json:"dependents,omitempty"`
	// Known is false when graph.json does not index this file — reported as
	// unknown rather than as a leaf, because the two are not the same claim.
	Known bool `json:"known"`
}

// BlastReport is the reach of what agents edited.
type BlastReport struct {
	// GraphPresent is false when no graph.json exists, in which case nothing
	// here can be scored at all.
	GraphPresent bool `json:"graphPresent"`
	// Percolates is the Molloy-Reed criterion (kappa >= 2): the dependency
	// graph has a cascade-capable core, so an edit to a hub propagates.
	Percolates bool         `json:"percolates"`
	Kappa      float64      `json:"kappa,omitempty"`
	Files      []EditedFile `json:"files,omitempty"`
	// Unknown counts edited files the graph does not index.
	Unknown int `json:"unknown"`
	// RunsScanned and Truncated describe the window this covers.
	RunsScanned int  `json:"runsScanned"`
	Truncated   bool `json:"truncated,omitempty"`
}

// AssessBlastRadius joins recent edit events against the code graph.
func AssessBlastRadius() BlastReport {
	var r BlastReport

	g, err := graph.Load(filepath.Join(config.ScriptHome(), "graph.json"))
	if err != nil || g == nil || g.Empty() {
		// No graph means no scoring is possible. Say so; do not emit a list
		// of files with a neutral radius that would read as "all safe".
		return r
	}
	r.GraphPresent = true
	r.Kappa = g.Kappa()
	r.Percolates = g.Percolates()

	runs, err := runlog.Runs() // newest first
	if err != nil {
		return r
	}
	if len(runs) > maxBlastRuns {
		runs, r.Truncated = runs[:maxBlastRuns], true
	}
	r.RunsScanned = len(runs)

	edits := map[string]int{}
	for _, id := range runs {
		events, err := runlog.Load(id)
		if err != nil {
			continue
		}
		for _, e := range events {
			// An edit event carries the edited file path in Agent — the field
			// is reused per kind, which is easy to misread.
			if e.Kind == runlog.KindEdit && e.Agent != "" {
				edits[e.Agent]++
			}
		}
	}

	for file, n := range edits {
		ef := EditedFile{File: file, Edits: n, Known: g.Knows(file)}
		if !ef.Known {
			r.Unknown++
			r.Files = append(r.Files, ef)
			continue
		}
		ef.Radius = g.BlastRadiusForFile(file)
		for _, id := range g.NodesInFile(file) {
			ef.Dependents += g.DependentCount(id)
		}
		r.Files = append(r.Files, ef)
	}

	// Widest reach first; unknown files sink to the bottom, where they read as
	// "not scored" rather than "scored low".
	sort.Slice(r.Files, func(i, j int) bool {
		if r.Files[i].Known != r.Files[j].Known {
			return r.Files[i].Known
		}
		if r.Files[i].Radius != r.Files[j].Radius {
			return r.Files[i].Radius > r.Files[j].Radius
		}
		return r.Files[i].File < r.Files[j].File
	})
	return r
}

// riskiestEdit returns the widest-reaching edited file, if any was scored.
func riskiestEdit(r BlastReport) (EditedFile, bool) {
	for _, f := range r.Files {
		if f.Known && f.Dependents > 0 {
			return f, true // the slice is already sorted widest-first
		}
	}
	return EditedFile{}, false
}

// blastCheck reports the reach of recent agent edits.
func blastCheck(r BlastReport) Check {
	const name = "Edit blast radius"
	if !r.GraphPresent {
		return Check{Name: name, Status: "no code graph",
			Detail: "graph.json is absent, so the reach of an agent's edits cannot be scored — " +
				"generate one with `hyctl graph` to enable this"}
	}
	if len(r.Files) == 0 {
		return Check{Name: name, Status: "no edits recorded",
			Detail: fmt.Sprintf("no agent edit was found in the %d most recent run(s)", r.RunsScanned)}
	}
	top, ok := riskiestEdit(r)
	if !ok {
		return Check{Name: name, Status: fmt.Sprintf("%d file(s), none with dependents", len(r.Files)),
			Detail: fmt.Sprintf("%d edited file(s) are not indexed by the graph, so their reach is unknown", r.Unknown)}
	}
	detail := fmt.Sprintf("widest reach: %s — %d dependent(s), radius %.2f×", top.File, top.Dependents, top.Radius)
	if r.Percolates {
		detail += fmt.Sprintf("; the graph percolates (kappa=%.1f), so an edit to a hub can cascade", r.Kappa)
	}
	if r.Unknown > 0 {
		detail += fmt.Sprintf("; %d edited file(s) are unindexed and unscored", r.Unknown)
	}
	return Check{Name: name, Status: fmt.Sprintf("%d file(s) scored", len(r.Files)-r.Unknown), Detail: detail}
}
