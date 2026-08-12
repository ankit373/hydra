// SPDX-License-Identifier: MIT

package security

import (
	"strings"
	"testing"
)

// With no graph nothing can be scored, and the check must say so rather than
// listing files at the neutral 1.0 floor — which would read as "all safe".
func TestBlastCheck_NoGraphSaysSoRatherThanScoringZero(t *testing.T) {
	c := blastCheck(BlastReport{})
	if c.Status != "no code graph" {
		t.Errorf("Status = %q, want it to name the missing graph", c.Status)
	}
	if !strings.Contains(c.Detail, "hyctl graph") {
		t.Errorf("Detail = %q, want it to say how to enable scoring", c.Detail)
	}
}

// A file the graph does not index is unknown, never low-risk — the whole
// reason internal/graph grew Knows().
func TestBlastCheck_UnindexedFilesAreUnknownNotSafe(t *testing.T) {
	r := BlastReport{
		GraphPresent: true, RunsScanned: 3, Unknown: 2,
		Files: []EditedFile{{File: "a.go", Edits: 1}, {File: "b.go", Edits: 1}},
	}
	c := blastCheck(r)
	if !strings.Contains(c.Status, "none with dependents") {
		t.Errorf("Status = %q, want it to avoid implying these were scored safe", c.Status)
	}
	if !strings.Contains(c.Detail, "unknown") {
		t.Errorf("Detail = %q, want the unknown reach stated", c.Detail)
	}
}

func TestRiskiestEdit_PrefersScoredFilesWithDependents(t *testing.T) {
	r := BlastReport{Files: []EditedFile{
		{File: "hub.go", Known: true, Dependents: 40, Radius: 6.3},
		{File: "leaf.go", Known: true, Dependents: 0, Radius: 1.0},
		{File: "unindexed.go"},
	}}
	top, ok := riskiestEdit(r)
	if !ok || top.File != "hub.go" {
		t.Fatalf("riskiestEdit = %+v/%v, want hub.go", top, ok)
	}

	// Nothing with dependents means nothing to point at.
	if _, ok := riskiestEdit(BlastReport{Files: []EditedFile{{File: "leaf.go", Known: true}}}); ok {
		t.Error("a leaf-only edit set produced a riskiest edit")
	}
}

// The action must actually reach the queue. An earlier round shipped a
// buildActions parameter whose block was never added, and because unused Go
// parameters are legal nothing failed — so assert the wiring, not just the
// helper.
func TestBuildActions_PercolatingGraphRaisesABlastAction(t *testing.T) {
	blast := BlastReport{
		GraphPresent: true, Percolates: true, Kappa: 3.4,
		Files: []EditedFile{{File: "internal/auth/token.go", Known: true, Dependents: 40, Radius: 6.3}},
	}
	actions := buildActions(Coverage{}, nil, nil, PolicyAudit{}, nil, EvidenceQuality{}, ConfigDrift{}, SupplyChain{}, blast)
	if len(actions) != 1 || actions[0].Kind != "blast" {
		t.Fatalf("actions = %+v, want one blast action", actions)
	}
	if !strings.Contains(actions[0].Title, "token.go") || !strings.Contains(actions[0].Title, "40") {
		t.Errorf("Title = %q, want the file and dependent count", actions[0].Title)
	}
}

// A graph that does not percolate has no cascade-capable core, so a wide edit
// is not automatically an incident.
func TestBuildActions_NonPercolatingGraphRaisesNothing(t *testing.T) {
	blast := BlastReport{
		GraphPresent: true, Percolates: false,
		Files: []EditedFile{{File: "a.go", Known: true, Dependents: 40, Radius: 6.3}},
	}
	if a := buildActions(Coverage{}, nil, nil, PolicyAudit{}, nil, EvidenceQuality{}, ConfigDrift{}, SupplyChain{}, blast); len(a) != 0 {
		t.Errorf("actions = %+v, want none without percolation", a)
	}
}

// Same wiring assertion for the supply-chain action.
func TestBuildActions_ChangedBinaryRaisesAnAction(t *testing.T) {
	sc := SupplyChain{Changed: 1, Binaries: []HeadBinary{
		{HeadID: "claude", Path: "/usr/local/bin/claude", Changed: true},
	}}
	actions := buildActions(Coverage{}, nil, nil, PolicyAudit{}, nil, EvidenceQuality{}, ConfigDrift{}, sc, BlastReport{})
	if len(actions) != 1 || actions[0].Kind != "supply-chain" || actions[0].Priority != PriorityNow {
		t.Fatalf("actions = %+v, want one PriorityNow supply-chain action", actions)
	}
}
