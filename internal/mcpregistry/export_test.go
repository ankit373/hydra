// SPDX-License-Identifier: MIT

package mcpregistry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExportDirectory_NoStatesWritesEmptyDirectory(t *testing.T) {
	withTempHydraHome(t)
	out := t.TempDir()

	n, err := ExportDirectory(out)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("n = %d, want 0", n)
	}
	if _, err := os.Stat(filepath.Join(out, "index.html")); err != nil {
		t.Errorf("index.html should still be written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "index.json")); err != nil {
		t.Errorf("index.json should still be written: %v", err)
	}
}

func TestExportDirectory_WritesOneEntryPerAuditedServer(t *testing.T) {
	withTempHydraHome(t)
	out := t.TempDir()

	if err := SaveStates(map[string]ServerState{
		"io.github.foo/bar": {
			State:          StateTrusted,
			StateChangedAt: time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
			LastScore:      Score{Overall: 82, Confidence: ConfidenceHigh},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeCache(&registryCache{Servers: []ServerRecord{
		{Name: "io.github.foo/bar", Repository: Repository{URL: "https://github.com/foo/bar"}},
	}}); err != nil {
		t.Fatal(err)
	}

	n, err := ExportDirectory(out)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("n = %d, want 1", n)
	}

	raw, err := os.ReadFile(filepath.Join(out, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []DirectoryEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	got := entries[0]
	if got.Name != "io.github.foo/bar" || got.LifecycleState != StateTrusted || got.RepositoryURL != "https://github.com/foo/bar" {
		t.Errorf("unexpected entry: %+v", got)
	}
	if got.Score.Overall != 82 {
		t.Errorf("Score.Overall = %v, want 82 (persisted LastScore should round-trip)", got.Score.Overall)
	}

	html, err := os.ReadFile(filepath.Join(out, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), "io.github.foo/bar") {
		t.Error("index.html should contain the server name")
	}
}

// The registry's own moderation policy is "minimal-to-no" review (design
// doc §2) — a malicious entry's name is untrusted input as far as this
// generator is concerned. html/template must escape it, not string-concat
// it into the page.
func TestExportDirectory_EscapesUntrustedServerNames(t *testing.T) {
	withTempHydraHome(t)
	out := t.TempDir()

	malicious := `<script>alert(1)</script>`
	if err := SaveStates(map[string]ServerState{
		malicious: {State: StateFlagged, LastScore: Score{Confidence: ConfidenceInsufficient}},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := ExportDirectory(out); err != nil {
		t.Fatal(err)
	}

	html, err := os.ReadFile(filepath.Join(out, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(html), "<script>alert(1)</script>") {
		t.Fatal("a malicious server name was rendered unescaped into the exported HTML")
	}
	if !strings.Contains(string(html), "&lt;script&gt;") {
		t.Error("expected the malicious name to appear HTML-escaped")
	}
}

func TestExportDirectory_EscapesUntrustedSignalDetail(t *testing.T) {
	withTempHydraHome(t)
	out := t.TempDir()

	if err := SaveStates(map[string]ServerState{
		"x": {State: StateTrusted, LastScore: Score{
			Confidence: ConfidenceHigh,
			SecurityImplementation: CategoryScore{Signals: []Signal{
				{Available: true, Detail: `<img src=x onerror=alert(1)>`},
			}},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := ExportDirectory(out); err != nil {
		t.Fatal(err)
	}
	html, err := os.ReadFile(filepath.Join(out, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(html), "<img src=x onerror=alert(1)>") {
		t.Fatal("an unescaped signal Detail was rendered into the exported HTML")
	}
}

func TestExportDirectory_SortedByName(t *testing.T) {
	withTempHydraHome(t)
	out := t.TempDir()

	if err := SaveStates(map[string]ServerState{
		"z-server": {State: StateTrusted},
		"a-server": {State: StateTrusted},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportDirectory(out); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(out, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []DirectoryEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name != "a-server" || entries[1].Name != "z-server" {
		t.Errorf("entries not sorted by name: %+v", entries)
	}
}
