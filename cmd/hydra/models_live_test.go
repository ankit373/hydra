// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/capabilities"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

func catalogue() []capabilities.Entry {
	return []capabilities.Entry{
		{ID: "claude", Name: "Claude Code", Provider: "anthropic", CapScore: 95, Source: "builtin"},
		{ID: "env/openai", Name: "OpenAI API", Provider: "openai", CapScore: 85, Source: "builtin"},
		{ID: "ollama", Name: "Ollama", Provider: "local", CapScore: 60, Source: "builtin"},
		{ID: "or-sonnet", Name: "Sonnet via OpenRouter", Provider: "openrouter", CapScore: 88, Source: "user"},
	}
}

// Three distinct states, and the defect was rendering all of them the same.
func TestModelRows_SeparatesLiveFromCatalogue(t *testing.T) {
	heads := []provider.Head{
		{ID: "claude", Name: "Claude Code"},
		{ID: "ollama", Name: "Ollama", LocalOnly: true}, // discovered, cannot serve
	}
	reason := func(h provider.Head) string {
		if h.ID == "ollama" {
			return "binary only, start its local server"
		}
		return ""
	}
	rows := modelRows(catalogue(), heads, reason)
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want one per catalogue entry", len(rows))
	}
	byID := map[string]modelRow{}
	for _, r := range rows {
		byID[r.ID] = r
	}

	if !byID["claude"].Live || byID["claude"].Why != "" {
		t.Errorf("a discovered, routable head is not live: %+v", byID["claude"])
	}
	// Discovered but refused: the reason must be the router's own, not invented.
	if byID["ollama"].Live {
		t.Error("a head that cannot serve is marked routable")
	}
	if byID["ollama"].Why != "binary only, start its local server" {
		t.Errorf("why = %q, want health.Reason's own text", byID["ollama"].Why)
	}
	// Never discovered at all: a different problem, so a different answer.
	for _, id := range []string{"env/openai", "or-sonnet"} {
		if byID[id].Live {
			t.Errorf("%s is marked routable but nothing discovered it", id)
		}
		if byID[id].Why != "not discovered on this machine" {
			t.Errorf("%s why = %q, want the not-discovered reason", id, byID[id].Why)
		}
	}
}

// The catalogue entry survives the join, so scores and source still render.
func TestModelRows_KeepsTheEntryIntact(t *testing.T) {
	rows := modelRows(catalogue(), nil, func(provider.Head) string { return "" })
	if rows[0].CapScore != 95 || rows[0].Source != "builtin" || rows[0].Provider != "anthropic" {
		t.Errorf("the entry was not carried through: %+v", rows[0])
	}
	if rows[3].Source != "user" {
		t.Errorf("a user entry lost its source: %+v", rows[3])
	}
}

func TestModelRows_NoCatalogueIsNoRows(t *testing.T) {
	if got := modelRows(nil, []provider.Head{{ID: "claude"}}, func(provider.Head) string { return "" }); len(got) != 0 {
		t.Errorf("got %d rows from an empty catalogue", len(got))
	}
}

// "added" alone read as "this is now routable", which an overlay entry never
// makes it. Each provider shape gets the step that would actually make it run.
func TestAddedModelNote_NamesWhatWouldMakeItRoutable(t *testing.T) {
	for _, tc := range []struct {
		name string
		e    capabilities.Entry
		want string
	}{
		{"api key provider", capabilities.Entry{ID: "env/mistral", Provider: "mistral"}, "API key"},
		{"local runtime", capabilities.Entry{ID: "my-llama", Provider: "ollama"}, "local server"},
		{"lm studio", capabilities.Entry{ID: "phi", Provider: "lmstudio"}, "local server"},
		{"anything else", capabilities.Entry{ID: "or-opus", Provider: "openrouter"}, "not a routable model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := addedModelNote(tc.e)
			if !strings.Contains(got, tc.want) {
				t.Errorf("note does not mention %q: %s", tc.want, got)
			}
			if !strings.Contains(got, tc.e.ID) {
				t.Errorf("note does not name the model: %s", got)
			}
			if !strings.Contains(got, "capability score") {
				t.Errorf("note does not say what was actually recorded: %s", got)
			}
		})
	}
}

func TestModelsList_ShowsRoutabilityAndACount(t *testing.T) {
	testutil.NewSandbox(t)
	out, _, err := run(t, "models", "list")
	if err != nil {
		t.Fatal(err)
	}
	out = stripANSICodes(out)
	if !strings.Contains(out, "ROUTABLE") {
		t.Errorf("no routability column:\n%s", out)
	}
	if !strings.Contains(out, "can be routed to right now") {
		t.Errorf("no count of what is actually available:\n%s", out)
	}
	// A sandbox has no API keys, so an env provider must not read as available.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "env/") && strings.Contains(line, "✓") {
			t.Errorf("an API provider with no key set is marked routable: %q", line)
		}
	}
}

// A caller reading JSON must not be misled either.
func TestModelsList_JSONCarriesTheDistinction(t *testing.T) {
	testutil.NewSandbox(t)
	out, _, err := run(t, "models", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"live":`) {
		t.Errorf("--json omits liveness, so a caller cannot tell:\n%s", out[:min(len(out), 400)])
	}
	if !strings.Contains(out, `"why":`) {
		t.Errorf("--json omits the reason a model is not routable:\n%s", out[:min(len(out), 400)])
	}
	// The score still has to be there; this is still the capability catalogue.
	if !strings.Contains(out, `"capScore":`) {
		t.Errorf("--json lost the capability score:\n%s", out[:min(len(out), 400)])
	}
}

func TestModelsAdd_SaysItRecordedAScoreNotAHead(t *testing.T) {
	testutil.NewSandbox(t)
	out, _, err := run(t, "models", "add", "or-opus", "--provider", "openrouter", "--cap-score", "90")
	if err != nil {
		t.Fatal(err)
	}
	out = stripANSICodes(out)
	if !strings.Contains(out, "recorded or-opus") {
		t.Errorf("output still claims a model was added:\n%s", out)
	}
	if !strings.Contains(out, "not a routable model") {
		t.Errorf("output does not say the model is not routable:\n%s", out)
	}
}
