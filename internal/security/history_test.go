// SPDX-License-Identifier: MIT

package security

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

func TestScoreHistory_FirstRunHasNoTrend(t *testing.T) {
	s := testutil.NewSandbox(t)
	path := filepath.Join(s.HydraHome, "security_score.jsonl")

	prior := loadScoreHistory(path)
	if len(prior) != 0 {
		t.Fatalf("a fresh sandbox already has history: %+v", prior)
	}
	trend := buildTrend(prior, Coverage{PercentCovered: 50})
	if trend.Available {
		t.Error("Trend.Available = true on the very first run, want false")
	}
}

func TestScoreHistory_RoundTripsAndComputesADelta(t *testing.T) {
	s := testutil.NewSandbox(t)
	path := filepath.Join(s.HydraHome, "security_score.jsonl")

	appendScoreHistory(path, Coverage{PercentCovered: 25, Applicable: 8, Covered: 2,
		Categories: []Category{{ID: "LLM03", Status: Gap}}})

	prior := loadScoreHistory(path)
	if len(prior) != 1 || prior[0].PercentCovered != 25 {
		t.Fatalf("loadScoreHistory = %+v, want one entry at 25%%", prior)
	}

	trend := buildTrend(prior, Coverage{PercentCovered: 75})
	if !trend.Available {
		t.Fatal("Trend.Available = false with prior history present")
	}
	if trend.DeltaPct != 50 {
		t.Errorf("DeltaPct = %v, want 50 (75 - 25)", trend.DeltaPct)
	}
	if trend.FirstPct != 25 {
		t.Errorf("FirstPct = %v, want 25", trend.FirstPct)
	}
}

// A corrupt history file must degrade to "no trend", never fail the report.
func TestScoreHistory_UnparseableLinesAreSkippedNotFatal(t *testing.T) {
	s := testutil.NewSandbox(t)
	path := filepath.Join(s.HydraHome, "security_score.jsonl")
	writeRaw(t, path, "not json\n{\"ts\":\"2026-01-01T00:00:00Z\",\"percentCovered\":40}\n")

	got := loadScoreHistory(path)
	if len(got) != 1 || got[0].PercentCovered != 40 {
		t.Errorf("loadScoreHistory = %+v, want the one valid line", got)
	}
}

func TestToHistoryPoints_StripsToTSAndPercent(t *testing.T) {
	entries := []scoreEntry{
		{TS: "2026-01-01T00:00:00Z", PercentCovered: 25, Applicable: 8, Covered: 2, Gaps: []string{"LLM03"}},
	}
	got := toHistoryPoints(entries)
	if len(got) != 1 || got[0].TS != "2026-01-01T00:00:00Z" || got[0].PercentCovered != 25 {
		t.Errorf("toHistoryPoints = %+v, want one point stripped to TS/PercentCovered", got)
	}
}

func writeRaw(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
