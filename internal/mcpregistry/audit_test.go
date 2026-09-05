// SPDX-License-Identifier: MIT

package mcpregistry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withStubbedScoringDeps points ComputeScore's external calls (OSV.dev,
// GitHub) at local httptest servers returning empty/neutral responses, so
// audit tests that resolve a server stay fast, offline, and deterministic,
// scoring's own behavior is covered separately in score_test.go.
func withStubbedScoringDeps(t *testing.T) {
	t.Helper()
	osv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(osv.Close)
	origOSV := osvQueryURL
	osvQueryURL = osv.URL
	t.Cleanup(func() { osvQueryURL = origOSV })

	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound) // non-GitHub-shaped repo URLs in these fixtures; 404 is fine, just needs to not hang
	}))
	t.Cleanup(gh.Close)
	origGH := githubAPIBase
	githubAPIBase = gh.URL
	t.Cleanup(func() { githubAPIBase = origGH })
}

func TestAudit_NeverSyncedReportsUnresolvedAndZeroSyncTime(t *testing.T) {
	withTempHydraHome(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	writeJSON(t, filepath.Join(home, ".cursor", "mcp.json"),
		`{"mcpServers": {"fetch": {"type":"stdio","command":"uvx","args":["mcp-server-fetch"]}}}`)

	rpt, err := Audit(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !rpt.RegistrySync.IsZero() {
		t.Errorf("expected zero RegistrySync when sync never ran, got %v", rpt.RegistrySync)
	}
	if len(rpt.Entries) != 1 || rpt.Entries[0].Status != StatusUnresolved {
		t.Fatalf("unexpected entries: %+v", rpt.Entries)
	}
}

func TestAudit_MatchesInstalledPackageAgainstSyncedRegistry(t *testing.T) {
	withTempHydraHome(t)
	withStubbedScoringDeps(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	writeJSON(t, filepath.Join(home, ".cursor", "mcp.json"),
		`{"mcpServers": {"fetch": {"type":"stdio","command":"uvx","args":["mcp-server-fetch"]}}}`)

	if err := writeCache(&registryCache{
		FetchedAt: time.Now().UTC(),
		Servers: []ServerRecord{
			{Name: "io.github.modelcontextprotocol/fetch", Packages: []Package{{RegistryType: "pypi", Identifier: "mcp-server-fetch"}}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	rpt, err := Audit(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if rpt.RegistrySync.IsZero() {
		t.Fatal("expected a non-zero RegistrySync after a cache write")
	}
	if len(rpt.Entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(rpt.Entries))
	}
	got := rpt.Entries[0]
	if got.Status != StatusVerified {
		t.Errorf("Status = %q, want verified", got.Status)
	}
	if got.RegistryName != "io.github.modelcontextprotocol/fetch" {
		t.Errorf("RegistryName = %q", got.RegistryName)
	}
	if got.Score == nil {
		t.Fatal("expected a Score for a verified entry")
	}
	if got.LifecycleState != StateProvisional {
		t.Errorf("LifecycleState = %q, want provisional (first time seen)", got.LifecycleState)
	}

	states, err := LoadStates()
	if err != nil {
		t.Fatal(err)
	}
	if states["io.github.modelcontextprotocol/fetch"].State != StateProvisional {
		t.Error("lifecycle state should be persisted across the audit run")
	}
}

func TestAudit_PersistsAliasesSoClassificationForToolWorksAfterward(t *testing.T) {
	withTempHydraHome(t)
	withStubbedScoringDeps(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	writeJSON(t, filepath.Join(home, ".cursor", "mcp.json"),
		`{"mcpServers": {"fetch": {"type":"stdio","command":"uvx","args":["mcp-server-fetch"]}}}`)

	if err := writeCache(&registryCache{
		FetchedAt: time.Now().UTC(),
		Servers: []ServerRecord{
			{Name: "io.github.modelcontextprotocol/fetch", Packages: []Package{{RegistryType: "pypi", Identifier: "mcp-server-fetch"}}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := Audit(context.Background(), ""); err != nil {
		t.Fatal(err)
	}

	// Phase 2's automaton starts a newly-seen clean server at PROVISIONAL,
	// not TRUSTED, so this shouldn't be classified yet, but it also
	// shouldn't be "never seen" any more, and that alias must now resolve.
	aliases, err := LoadAliases()
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := aliases["fetch"]
	if !ok || entry.RegistryName != "io.github.modelcontextprotocol/fetch" || entry.Status != StatusVerified {
		t.Fatalf("alias not persisted correctly: %+v (ok=%v)", entry, ok)
	}
	if _, flagged := ClassificationForTool("mcp__fetch__something"); flagged {
		t.Error("a freshly-provisional server should not be flagged")
	}
}

func TestAudit_CorruptCacheFileIsAnError(t *testing.T) {
	withTempHydraHome(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.WriteFile(cachePath(), []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Audit(context.Background(), ""); err == nil {
		t.Fatal("a corrupt registry cache must fail the audit, not silently proceed with no registry data")
	}
}

func TestAudit_CorruptStateFileIsAnError(t *testing.T) {
	withTempHydraHome(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.WriteFile(statePath(), []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Audit(context.Background(), ""); err == nil {
		t.Fatal("a corrupt lifecycle-state file must fail the audit, not silently discard history")
	}
}

func TestAudit_CorruptAliasesFileIsAnError(t *testing.T) {
	withTempHydraHome(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.WriteFile(aliasPath(), []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Audit(context.Background(), ""); err == nil {
		t.Fatal("a corrupt aliases file must fail the audit, not silently discard history")
	}
}

func TestAudit_UnresolvedEntryGetsNearestMatchHint(t *testing.T) {
	withTempHydraHome(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	writeJSON(t, filepath.Join(home, ".cursor", "mcp.json"),
		`{"mcpServers": {"chrome": {"type":"stdio","command":"npx","args":["-y","chrome-mcp"]}}}`)

	if err := writeCache(&registryCache{
		FetchedAt: time.Now().UTC(),
		Servers:   []ServerRecord{{Name: "x", Packages: []Package{{Identifier: "chrome-devtools-mcp"}}}},
	}); err != nil {
		t.Fatal(err)
	}

	rpt, err := Audit(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rpt.Entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(rpt.Entries))
	}
	got := rpt.Entries[0]
	if got.Status != StatusUnresolved {
		t.Fatalf("Status = %q, want unresolved", got.Status)
	}
	if got.NearestMatch != "chrome-devtools-mcp" || got.NearestDist < 0 {
		t.Errorf("NearestMatch/NearestDist = %q/%d, want a hint toward chrome-devtools-mcp", got.NearestMatch, got.NearestDist)
	}
}

func TestAudit_RemoteServerIsNeverMarkedVerified(t *testing.T) {
	withTempHydraHome(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	writeJSON(t, filepath.Join(home, ".cursor", "mcp.json"),
		`{"mcpServers": {"remote": {"type":"http","url":"https://example.com/mcp"}}}`)

	if err := writeCache(&registryCache{Servers: []ServerRecord{{Name: "x"}}}); err != nil {
		t.Fatal(err)
	}

	rpt, err := Audit(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rpt.Entries) != 1 || rpt.Entries[0].Status != StatusUnresolved {
		t.Fatalf("remote server should be unresolved (Phase 1 does not match URLs), got %+v", rpt.Entries)
	}
}

func TestBuildPackageIndex(t *testing.T) {
	idx := buildPackageIndex([]ServerRecord{
		{Name: "a", Packages: []Package{{Identifier: "pkg-a"}}},
		{Name: "b", Packages: []Package{{Identifier: "pkg-b"}, {Identifier: "pkg-b-alt"}}},
	})
	if len(idx) != 3 {
		t.Fatalf("got %d entries, want 3", len(idx))
	}
	if idx["pkg-a"].Name != "a" || idx["pkg-b"].Name != "b" || idx["pkg-b-alt"].Name != "b" {
		t.Errorf("unexpected index contents: %+v", idx)
	}
}
