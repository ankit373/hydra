// SPDX-License-Identifier: MIT

package mcpregistry

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAudit_NeverSyncedReportsUnresolvedAndZeroSyncTime(t *testing.T) {
	withTempHydraHome(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	writeJSON(t, filepath.Join(home, ".cursor", "mcp.json"),
		`{"mcpServers": {"fetch": {"type":"stdio","command":"uvx","args":["mcp-server-fetch"]}}}`)

	rpt, err := Audit("")
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

	rpt, err := Audit("")
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

	rpt, err := Audit("")
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
