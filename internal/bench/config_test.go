// SPDX-License-Identifier: MIT

package bench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testConfig = `
tier = "smoke"
track = "core"
suites = ["fake"]
n_test = 5
seed = 1
max_usd = 1.0
arms = ["frontier", "cheapest", "static-fallback"]
[models]
frontier = "openrouter:m-front"
cheapest = "openrouter:m-cheap"
[static_fallback]
arms = ["frontier", "cheapest"]
fault_rate = 0.1
[price_refs]
"m-front" = "ref/front"
[prices."m-cheap"]
in_per_mtok = 0.035
out_per_mtok = 0.14
source = "https://aws.amazon.com/bedrock/pricing/"
date = "2026-11-05"
[cutoffs]
"m-front" = "2025-03"
"m-cheap" = "unstated"
`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(p, []byte(body), 0o644)
	return p
}

func TestReadConfig_ResolvesArmsModelsAndPrices(t *testing.T) {
	c, err := ReadConfig(writeConfig(t, testConfig))
	if err != nil {
		t.Fatal(err)
	}
	arms, _ := c.ArmConfigs()
	if fb := arms[StaticFallback]; len(fb.Models) != 2 || fb.FaultRate != 0.1 || fb.Models[0] != "openrouter:m-front" {
		t.Fatalf("static-fallback = %+v", fb)
	}
	if ids := c.ModelIDs(); len(ids) != 2 || ids[0] != "m-cheap" {
		t.Fatalf("models = %v", ids)
	}
	o, err := c.Overrides()
	if err != nil || o["m-cheap"].Source != "config:https://aws.amazon.com/bedrock/pricing/" || o["m-cheap"].FetchedAt.Year() != 2026 {
		t.Fatalf("overrides = %+v, %v", o, err)
	}
}

func TestReadConfig_RefusesWhatARunCouldNotAccountFor(t *testing.T) {
	for name, edit := range map[string][2]string{
		"unknown key":         {"seed = 1", "seed = 1\nconcurrency = 4"},
		"no arms":             {`arms = ["frontier", "cheapest", "static-fallback"]`, "arms = []"},
		"arm with no model":   {`arms = ["frontier", "cheapest", "static-fallback"]`, `arms = ["mid"]`},
		"arm listed twice":    {`arms = ["frontier", "cheapest", "static-fallback"]`, `arms = ["frontier", "frontier"]`},
		"unpinned model":      {`frontier = "openrouter:m-front"`, `frontier = "bedrock:<opus id>"`},
		"one-model fallback":  {`arms = ["frontier", "cheapest"]` + "\nfault_rate", `arms = ["frontier"]` + "\nfault_rate"},
		"fallback to nothing": {`arms = ["frontier", "cheapest"]` + "\nfault_rate", `arms = ["frontier", "mid"]` + "\nfault_rate"},
		"bad fault rate":      {"fault_rate = 0.1", "fault_rate = 2"},
		"no cutoff":           {`"m-cheap" = "unstated"`, ""},
		"not toml":            {"tier =", "tier = = ="},
	} {
		body := strings.Replace(testConfig, edit[0], edit[1], 1)
		if body == testConfig {
			t.Fatalf("%s: the edit did not apply", name)
		}
		if _, err := ReadConfig(writeConfig(t, body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	noFallback := strings.Replace(testConfig, "[static_fallback]\narms = [\"frontier\", \"cheapest\"]\nfault_rate = 0.1\n", "", 1)
	if _, err := ReadConfig(writeConfig(t, noFallback)); err == nil {
		t.Error("static-fallback with no [static_fallback] accepted")
	}
	undated := &Config{Prices: map[string]PriceOverride{"m": {InPerM: 1, Source: "x"}}}
	if _, err := undated.Overrides(); err == nil {
		t.Error("an undated price override accepted")
	}
}

// The committed smoke config waits on Q4 and nothing else: with its two placeholder
// ids pinned, it loads.
func TestCommittedSmokeConfig_WaitsOnlyOnQ4(t *testing.T) {
	path := "../../bench/configs/smoke.toml"
	if _, err := ReadConfig(path); err == nil || !strings.Contains(err.Error(), "not a pinned") {
		t.Fatalf("smoke.toml loaded with placeholder ids, or failed for another reason: %v", err)
	}
	b, _ := os.ReadFile(path)
	body := strings.NewReplacer(
		"bedrock:<strongest Opus id enabled in the account's region>", "bedrock:opus-x",
		"bedrock:<Claude Haiku 4.5 id>", "bedrock:haiku-x",
		"[cutoffs]", "[cutoffs]\n\"opus-x\" = \"q4\"\n\"haiku-x\" = \"q4\"",
	).Replace(string(b))
	c, err := ReadConfig(writeConfig(t, body))
	if err != nil {
		t.Fatalf("smoke.toml with ids pinned: %v", err)
	}
	if c.Tier != "smoke" || c.NTest != 100 || c.MaxUSD*float64(len(c.Suites)) > 5 {
		t.Fatalf("smoke tier is %s, n=%d, $%v × %d suites; want 100 items under $5", c.Tier, c.NTest, c.MaxUSD, len(c.Suites))
	}
}
