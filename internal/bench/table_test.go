// SPDX-License-Identifier: MIT

package bench

import (
	"os"
	"strings"
	"testing"
	"time"
)

func tableSummary(tier, licence string) Summary {
	cpa := 0.0032512345
	return Summary{
		Schema: summarySchema, RunID: "core-banking77-" + tier + "-s1-3f9a2c1d", Tier: tier,
		Breadcrumb: Breadcrumb{Suite: "banking77", GitSHA: "abc123", LicenceVerified: licence,
			Prices: PriceSnapshot{"m": {FetchedAt: time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)}}},
		Arms: map[string]ArmSummary{
			Frontier:    {N: 1500, PassRate: 0.931, PassWilson: [2]float64{0.917, 0.943}, CostPerAccepted: &cpa},
			"cheapest":  {N: 1500, PassRate: 0.8, PassWilson: [2]float64{0.779, 0.82}},
			OracleArm:   {N: 1500, PassRate: 0.95, PassWilson: [2]float64{0.938, 0.96}},
			"static-fb": {N: 1500, PassRate: 0.9, PassWilson: [2]float64{0.88, 0.92}},
		},
		Paired: map[string]PairedStats{"cheapest-vs-frontier": {DeltaPass: -0.131, BootLower95: f64(-0.14)}},
	}
}

// The table matches a golden file, frontier first and the oracle last.
func TestTable_Golden(t *testing.T) {
	got := Table([]Summary{tableSummary("full", "https://x/LICENSE, 2026-11-05")})
	want, err := os.ReadFile("testdata/table.golden.md")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("table drifted from testdata/table.golden.md:\n%s", got)
	}
}

// A smoke run is never a verdict, and an unverified licence is never publishable.
func TestTable_SmokeAndUnverifiedLicenceAreNeverVerdicts(t *testing.T) {
	if got := Table([]Summary{tableSummary("smoke", "y")}); strings.Count(got, "not a verdict (smoke)") != 4 {
		t.Fatalf("smoke rows:\n%s", got)
	}
	if got := Table([]Summary{tableSummary("full", "")}); strings.Count(got, "unpublishable (licence)") != 4 {
		t.Fatalf("unverified rows:\n%s", got)
	}
	// Incomplete outranks both: it is the first thing a reader must know.
	s := tableSummary("smoke", "")
	s.Incomplete, s.Unscored = true, 3
	if got := Table([]Summary{s}); strings.Count(got, "INCOMPLETE (3 unscored)") != 4 {
		t.Fatalf("incomplete rows:\n%s", got)
	}
}

func f64(v float64) *float64 { return &v }
