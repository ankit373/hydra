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

func TestDirectory_NoStatesIsEmptyNotNil(t *testing.T) {
	withTempHydraHome(t)
	entries, err := Directory()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("got %d entries, want 0", len(entries))
	}
}

func TestDirectory_MatchesWhatExportWrites(t *testing.T) {
	withTempHydraHome(t)
	if err := SaveStates(map[string]ServerState{
		"io.github.foo/bar": {State: StateTrusted, LastScore: Score{Overall: 91, Confidence: ConfidenceHigh}},
	}); err != nil {
		t.Fatal(err)
	}

	viaDirectory, err := Directory()
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	n, err := ExportDirectory(out)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(viaDirectory) {
		t.Errorf("ExportDirectory wrote %d entries, Directory() returned %d — they should agree", n, len(viaDirectory))
	}

	raw, err := os.ReadFile(filepath.Join(out, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var exported []DirectoryEntry
	if err := json.Unmarshal(raw, &exported); err != nil {
		t.Fatal(err)
	}
	if len(exported) != 1 || exported[0].Name != viaDirectory[0].Name || exported[0].Score.Overall != viaDirectory[0].Score.Overall {
		t.Errorf("exported = %+v, Directory() = %+v — should be the same data", exported, viaDirectory)
	}
}

func TestExportDirectory_PropagatesDirectoryError(t *testing.T) {
	withTempHydraHome(t)
	if err := os.WriteFile(statePath(), []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportDirectory(t.TempDir()); err == nil {
		t.Fatal("a corrupt state file should fail the export, not silently write an empty directory")
	}
}

// A file sitting where the output directory needs to go is a portable way
// to make MkdirAll fail on every OS, without relying on permission bits
// (which Windows doesn't enforce the same way — see internal/config's
// equivalent test for the same reasoning).
func TestExportDirectory_MkdirAllFailureIsAnError(t *testing.T) {
	withTempHydraHome(t)
	parent := t.TempDir()
	blocked := filepath.Join(parent, "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// outDir itself is a file, so MkdirAll(outDir, ...) must fail: it can't
	// turn a file into a directory.
	if _, err := ExportDirectory(blocked); err == nil {
		t.Fatal("expected an error when outDir already exists as a regular file")
	}
}

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

// §13.5 of the design doc: publishing a public negative signal about a real
// project needs a disclaimer and a correction path before it ships, not as
// an afterthought.
func TestExportDirectory_IncludesDisclaimerBanner(t *testing.T) {
	withTempHydraHome(t)
	out := t.TempDir()
	if _, err := ExportDirectory(out); err != nil {
		t.Fatal(err)
	}
	html, err := os.ReadFile(filepath.Join(out, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), "not a guarantee of safety") {
		t.Error("exported page must carry the probabilistic-signal disclaimer")
	}
}

func TestExportDirectory_EachEntryHasADisputeLink(t *testing.T) {
	withTempHydraHome(t)
	if err := SaveStates(map[string]ServerState{
		"io.github.foo/bar": {State: StateFlagged, LastScore: Score{Confidence: ConfidenceInsufficient}},
	}); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
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
	if len(entries) != 1 || entries[0].DisputeURL == "" {
		t.Fatalf("expected a non-empty DisputeURL, got %+v", entries)
	}
	if !strings.Contains(entries[0].DisputeURL, "github.com/ankit373/hydra/issues/new") {
		t.Errorf("DisputeURL = %q, expected a link to this repo's issue tracker", entries[0].DisputeURL)
	}
	if !strings.Contains(entries[0].DisputeURL, "template=mcp_registry_dispute.md") {
		t.Errorf("DisputeURL = %q, expected it to use the dispute issue template", entries[0].DisputeURL)
	}

	html, err := os.ReadFile(filepath.Join(out, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), "dispute this score") {
		t.Error("exported page should render a dispute link per row")
	}
}

// A malicious server name flows into the dispute URL's query string too —
// html/template's URL-context escaping must hold there as well, not just in
// the visible table cell.
func TestExportDirectory_DisputeURLEscapesUntrustedServerName(t *testing.T) {
	withTempHydraHome(t)
	malicious := `foo"><script>alert(1)</script>`
	if err := SaveStates(map[string]ServerState{
		malicious: {State: StateFlagged, LastScore: Score{Confidence: ConfidenceInsufficient}},
	}); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if _, err := ExportDirectory(out); err != nil {
		t.Fatal(err)
	}
	html, err := os.ReadFile(filepath.Join(out, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(html), "<script>alert(1)</script>") {
		t.Fatal("a malicious server name leaked unescaped into the dispute link's href")
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
