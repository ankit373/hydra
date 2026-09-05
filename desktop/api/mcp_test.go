// SPDX-License-Identifier: MIT

package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

// A machine with no MCP clients configured must report that it looked, not an
// empty list that reads as a finding.
func TestGetMCPServers_EmptyMachineSaysItScanned(t *testing.T) {
	testutil.NewSandbox(t)

	got := New().GetMCPServers()
	if !got.Scanned {
		t.Errorf("Scanned = false with no error: %+v", got)
	}
	if got.Servers == nil {
		t.Error("Servers is nil — the bridge must send [] for an empty list")
	}
	if len(got.Servers) != 0 {
		t.Errorf("expected no servers in a sandbox, got %+v", got.Servers)
	}
}

// Without a sync every server reads unresolved, and a list of unresolved
// servers is not a finding about those servers. The view has to be able to say
// which case it is in.
func TestGetMCPServers_NeverSyncedIsReported(t *testing.T) {
	testutil.NewSandbox(t)

	if got := New().GetMCPServers(); got.Synced != "" {
		t.Errorf("Synced = %q on a machine that never synced", got.Synced)
	}
}

// Scan reads client configs, so this proves the shape it produces reaches the
// panel — and that the identity fields survive the mapping.
func TestGetMCPServers_ReportsAnInstalledServer(t *testing.T) {
	s := testutil.NewSandbox(t)

	// The Claude Code config shape: mcpServers keyed by name. The env value
	// here is a canary — Scan is identity-only by construction, so it must not
	// appear anywhere in the panel.
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"postmark": map[string]any{
				"command": "npx",
				"args":    []string{"-y", "postmark-mcp"},
				"env":     map[string]string{"POSTMARK_TOKEN": "SUPER-SECRET-CANARY"},
			},
		},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Home, ".claude.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	got := New().GetMCPServers()
	if !got.Scanned {
		t.Fatalf("scan did not run: %+v", got)
	}
	// The secret must not have travelled, asserted before anything that could
	// skip: on a platform where Scan finds nothing this is trivially true, but
	// it must never be the assertion that silently stops running. Checked
	// against the whole marshalled panel rather than field by field, so a
	// future field cannot leak it either.
	blob, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{"SUPER-SECRET-CANARY", "POSTMARK_TOKEN"} {
		if strings.Contains(string(blob), canary) {
			t.Errorf("%q from a client config reached the desktop panel", canary)
		}
	}

	if len(got.Servers) == 0 {
		t.Skip("this platform's Scan found no servers for the fixture config")
	}

	var found *MCPServer
	for i := range got.Servers {
		if got.Servers[i].Name == "postmark" {
			found = &got.Servers[i]
		}
	}
	if found == nil {
		t.Fatalf("the configured server is missing: %+v", got.Servers)
	}
	if found.Command != "npx" {
		t.Errorf("Command = %q, want npx", found.Command)
	}
	// With no sync there is nothing to resolve against.
	if found.Status != "unresolved" {
		t.Errorf("Status = %q, want unresolved with no sync", found.Status)
	}
	// A score with no evidence must not render as a zero.
	if found.Scored {
		t.Errorf("Scored = true with no registry data: %+v", found)
	}

}

// Sync is a network fetch. In a sandbox with no network it must report the
// failure rather than panic or hang past its timeout.
func TestSyncMCPRegistry_ReportsFailureRatherThanHanging(t *testing.T) {
	testutil.NewSandbox(t)

	got := New().SyncMCPRegistry()
	if got.Error == "" && got.Servers == 0 {
		t.Error("sync reported neither servers nor an error")
	}
}
