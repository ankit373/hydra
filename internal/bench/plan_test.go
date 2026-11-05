// SPDX-License-Identifier: MIT

package bench

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPlan_FreezesItemsArmsPricesAndABreadcrumb(t *testing.T) {
	c, err := ReadConfig(writeConfig(t, testConfig))
	if err != nil {
		t.Fatal(err)
	}
	splits := filepath.Join(t.TempDir(), "fake.tsv")
	os.WriteFile(splits, []byte("id\tsplit\trank\tlabel\n"), 0o644)
	var all []Item
	for _, it := range fakeItems(9) {
		all = append(all, it)
	}
	all = append(all, Item{ID: "cal/0", Split: SplitCalibration, Rank: 0})
	prices := testPrices("m-front", "m-cheap")
	host := HostInfo{OS: "darwin", Arch: "arm64", CPUs: 8, TotalRAMGB: 16, FreeRAMGB: 4, MemPressure: "low"}
	ds := Dataset{Revision: "r@1", Files: []LockEntry{{Name: "f", SHA256: "h"}}}
	run, items, err := Plan(c, fakeSuite{}, ds, splits, all, 5, nil, prices, host)
	if err != nil {
		t.Fatal(err)
	}
	want := FirstByRank(all, SplitTest, 5)
	if len(run.ItemIDs) != 5 || run.ItemIDs[0] != want[0].ID || len(items) != 5 {
		t.Fatalf("items %v, want the first 5 test items by rank", run.ItemIDs)
	}
	if !reflect.DeepEqual(run.ArmOrder, c.Arms) || len(run.Prices) != 2 {
		t.Fatalf("arms %v prices %v", run.ArmOrder, run.Prices)
	}
	// Direct-only: every breadcrumb field but Config, CalibSHA256 and the provider
	// models (read off the rows by report) is set before the first call.
	b := run.Crumb
	for name, empty := range map[string]bool{
		"harness_version": b.HarnessVersion == "", "git_sha": b.GitSHA == "", "suite": b.Suite == "",
		"suite_revision": b.SuiteRevision == "", "data_sha256": len(b.DataSHA256) == 0,
		"split_sha256": b.SplitSHA256 == "", "arms": len(b.Arms) == 0, "prices": len(b.Prices) == 0,
		"host": b.Host == (HostInfo{}), "model_cutoffs": len(b.ModelCutoffs) != 2, "seed": b.Seed == 0,
	} {
		if empty {
			t.Errorf("breadcrumb %s is empty", name)
		}
	}

	one, _, err := Plan(c, fakeSuite{}, ds, splits, all, 5, []string{"cheapest"}, prices, host)
	if err != nil || len(one.Arms) != 1 || len(one.Prices) != 1 || one.cfg8() == run.cfg8() {
		t.Fatalf("--arms cheapest: %+v, %v", one.ArmOrder, err)
	}
	for name, args := range map[string]struct {
		arms   []string
		prices PriceSnapshot
		all    []Item
		splits string
	}{
		"unknown arm":  {[]string{"mid"}, prices, all, splits},
		"no price":     {nil, testPrices("m-front"), all, splits},
		"no test item": {nil, prices, all[len(all)-1:], splits},
		"no split":     {nil, prices, all, filepath.Join(t.TempDir(), "none")},
	} {
		if _, _, err := Plan(c, fakeSuite{}, ds, args.splits, args.all, 5, args.arms, args.prices, host); err == nil {
			t.Errorf("%s: planned", name)
		}
	}

	arms, err := BuildArms(run, newFakeExec().For)
	if err != nil || len(arms) != 3 || arms[StaticFallback].(*Direct).faults == nil || arms["frontier"].(*Direct).faults != nil {
		t.Fatalf("BuildArms = %v, %v; want a fault plan on static-fallback only", arms, err)
	}
	run.Arms["frontier"] = ArmConfig{Models: []string{"openrouter:unpriced"}}
	if _, err := BuildArms(run, newFakeExec().For); err == nil {
		t.Fatal("built an arm whose model has no price")
	}
}

// cfg8 ignores the cap and a price's fetch time, and nothing else that matters.
func TestCfg8_WhatChangesTheRun(t *testing.T) {
	base := Run{Track: "core", Suite: "s", Tier: "smoke", Seed: 1, ItemIDs: []string{"a"}, Prices: testPrices("m")}
	same := base
	same.MaxUSDHistory = []float64{9}
	same.Prices = testPrices("m")
	p := same.Prices["m"]
	p.FetchedAt = p.FetchedAt.AddDate(0, 0, 1)
	same.Prices["m"] = p
	if same.cfg8() != base.cfg8() {
		t.Fatal("the cap or a fetch time changed the run id")
	}
	diff := base
	diff.Prices = testPrices("m")
	p.InPerM++
	diff.Prices["m"] = p
	if diff.cfg8() == base.cfg8() {
		t.Fatal("a changed price kept the run id")
	}
}
