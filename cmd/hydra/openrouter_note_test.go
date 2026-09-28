// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

// #717: "12 of 327 models enabled" is honest. A list silently cut to a dozen,
// or a bare count with no denominator, is the #714 defect.
func TestOpenRouterNote_NamesBothNumbers(t *testing.T) {
	got := openRouterNote(12, 327, "/h/config.toml")
	for _, want := range []string{"12", "327", "/h/config.toml", "[openrouter]"} {
		if !strings.Contains(got, want) {
			t.Errorf("note omits %q: %s", want, got)
		}
	}
}

// No allowlist means the single key-derived head, which is what it always was.
// There is nothing to explain, so nothing is printed.
func TestOpenRouterNote_SilentWithNoAllowlist(t *testing.T) {
	if got := openRouterNote(0, 327, "/h/config.toml"); got != "" {
		t.Errorf("note = %q, want nothing said when no models are named", got)
	}
}

// An unfetched catalogue has an unknown size. "3 of 0" would read as a
// catalogue with nothing in it, which is a different and wrong claim.
func TestOpenRouterNote_UnfetchedCatalogueHasNoDenominator(t *testing.T) {
	got := openRouterNote(3, 0, "/h/config.toml")
	if strings.Contains(got, "of 0") {
		t.Errorf("an unfetched catalogue rendered as a size of zero: %s", got)
	}
	if !strings.Contains(got, "3") {
		t.Errorf("note does not say how many are enabled: %s", got)
	}
	if !strings.Contains(got, "not been fetched") {
		t.Errorf("note does not say why the denominator is missing: %s", got)
	}
}

// The catalogue is only read when there is something to count against, so a
// machine with no allowlist does not pay for the note it will not print.
func TestOpenRouterCounts_NoAllowlistReadsNoCatalogue(t *testing.T) {
	testutil.NewSandbox(t)

	enabled, catalogue := openRouterCounts()
	if enabled != 0 {
		t.Errorf("enabled = %d with no config file", enabled)
	}
	if catalogue != 0 {
		t.Errorf("catalogue = %d, want 0: it should not have been read at all", catalogue)
	}
}

func TestOpenRouterCounts_CountsTheConfiguredModels(t *testing.T) {
	s := testutil.NewSandbox(t)
	body := "[openrouter]\nmodels = [\"a/one\", \"b/two\", \"a/one\"]\n"
	if err := os.WriteFile(filepath.Join(s.HydraHome, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if enabled, _ := openRouterCounts(); enabled != 2 {
		t.Errorf("enabled = %d, want 2, the duplicate is one model", enabled)
	}
}

// probe is the discovery surface, so this is where the count belongs, and it
// must list *every* named model.
//
// The end-to-end path is what proves this: with per-model heads emitted
// correctly, rank.ByCapScore still keyed non-local heads on their provider, so
// three named models arrived at probe as one, whichever scored highest, and the
// other two were gone from probe, status and routing alike.
func TestCLI_Probe_ListsEveryAllowlistedModel(t *testing.T) {
	s := testutil.NewSandbox(t)
	s.SetKey(t, "OPENROUTER_API_KEY", "sk-test")
	// Deliberately different scores, since the collapse kept the highest and a
	// same-score pair could survive it by accident.
	models := []string{"anthropic/claude-opus-4.1", "google/gemini-2.5-flash", "meta-llama/llama-3.2-1b"}
	body := "[openrouter]\nmodels = [\"" + strings.Join(models, "\", \"") + "\"]\n"
	if err := os.WriteFile(filepath.Join(s.HydraHome, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	out, _, err := run(t, "probe")
	if err != nil {
		t.Fatal(err)
	}
	out = stripANSICodes(out)
	for _, m := range models {
		if !strings.Contains(out, m+" (OpenRouter)") {
			t.Errorf("%s is not listed; the named heads collapsed to one per provider:\n%s", m, out)
		}
	}
	if !strings.Contains(out, "OpenRouter models enabled") {
		t.Errorf("probe does not say how many models the allowlist admits:\n%s", out)
	}
}

// Without an allowlist probe must look exactly as it did, so an upgrade adds
// no line to a surface #714 was about keeping readable.
func TestCLI_Probe_SaysNothingWithoutAnAllowlist(t *testing.T) {
	s := testutil.NewSandbox(t)
	s.SetKey(t, "OPENROUTER_API_KEY", "sk-test")

	out, _, err := run(t, "probe")
	if err != nil {
		t.Fatal(err)
	}
	out = stripANSICodes(out)
	if strings.Contains(out, "OpenRouter models enabled") {
		t.Errorf("probe volunteered an allowlist note with no allowlist:\n%s", out)
	}
	if !strings.Contains(out, "OpenRouter") {
		t.Errorf("the single key-derived head disappeared:\n%s", out)
	}
}

// A JSON caller cannot derive the catalogue size from the head list, so it
// would have no way to tell a 12-model allowlist from all there is.
func TestCLI_Probe_JSONCarriesTheCounts(t *testing.T) {
	s := testutil.NewSandbox(t)
	s.SetKey(t, "OPENROUTER_API_KEY", "sk-test")
	body := "[openrouter]\nmodels = [\"anthropic/claude-sonnet-4.5\"]\n"
	if err := os.WriteFile(filepath.Join(s.HydraHome, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	out, _, err := run(t, "probe", "--json")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"openrouter"`, `"enabled":1`, `"catalogue"`} {
		if !strings.Contains(out, want) {
			t.Errorf("--json omits %s:\n%s", want, out[:min(len(out), 600)])
		}
	}
}
