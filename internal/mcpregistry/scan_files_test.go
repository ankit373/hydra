// SPDX-License-Identifier: MIT

package mcpregistry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeJSON(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestScanMCPServersFile_MissingFileIsNotAnError(t *testing.T) {
	out := scanMCPServersFile(filepath.Join(t.TempDir(), "nope.json"), "cursor", "user")
	if out != nil {
		t.Errorf("expected nil for a missing file, got %v", out)
	}
}

func TestScanMCPServersFile_ParsesEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	writeJSON(t, path, `{
		"mcpServers": {
			"fetch": {"type":"stdio","command":"uvx","args":["mcp-server-fetch"],"env":{"TOKEN":"secret"}},
			"remote": {"type":"http","url":"https://example.com/mcp"}
		}
	}`)

	out := scanMCPServersFile(path, "cursor", "user")
	if len(out) != 2 {
		t.Fatalf("got %d entries, want 2", len(out))
	}

	byName := map[string]InstalledServer{}
	for _, s := range out {
		byName[s.Name] = s
	}
	if byName["fetch"].Package != "mcp-server-fetch" {
		t.Errorf("fetch.Package = %q, want mcp-server-fetch", byName["fetch"].Package)
	}
	if !byName["remote"].Remote {
		t.Errorf("remote server should have Remote=true")
	}
	if byName["fetch"].Client != "cursor" || byName["fetch"].Scope != "user" {
		t.Errorf("fetch client/scope = %q/%q, want cursor/user", byName["fetch"].Client, byName["fetch"].Scope)
	}
}

func TestScanVSCodeFile_UsesServersKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	writeJSON(t, path, `{"servers": {"gh": {"type":"stdio","command":"npx","args":["-y","@modelcontextprotocol/server-github"]}}}`)

	out := scanVSCodeFile(path)
	if len(out) != 1 {
		t.Fatalf("got %d entries, want 1", len(out))
	}
	if out[0].Package != "@modelcontextprotocol/server-github" {
		t.Errorf("Package = %q", out[0].Package)
	}
	if out[0].Client != "vscode" {
		t.Errorf("Client = %q, want vscode", out[0].Client)
	}
}

func TestScanClaudeCode_UserAndProjectScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	claudeJSON := filepath.Join(home, ".claude.json")
	proj := filepath.Join(home, "myproject")

	// Built structurally, not by string-interpolating proj into a JSON
	// literal: proj is a Windows path on Windows CI, and an unescaped
	// backslash inside a hand-written JSON string corrupts the document —
	// this is exactly the bug that broke this test on windows-latest.
	cfg := claudeCodeConfig{
		MCPServers: map[string]clientServerConfig{
			"grafana": {Type: "stdio", Command: "npx", Args: []string{"-y", "mcp-grafana"}},
		},
		Projects: map[string]struct {
			MCPServers map[string]clientServerConfig `json:"mcpServers"`
		}{
			proj: {MCPServers: map[string]clientServerConfig{
				"custom": {Type: "stdio", Command: "node", Args: []string{"./server.js"}},
			}},
		},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writeJSON(t, claudeJSON, string(raw))

	out := scanClaudeCode(proj)
	if len(out) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(out), out)
	}

	var sawUser, sawProject bool
	for _, s := range out {
		if s.Name == "grafana" && s.Scope == "user" {
			sawUser = true
		}
		if s.Name == "custom" && s.Scope == "project" {
			sawProject = true
		}
	}
	if !sawUser || !sawProject {
		t.Errorf("expected one user-scope and one project-scope entry, got %+v", out)
	}
}

func TestScanClaudeCode_DifferentCwdSkipsProjectServers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	claudeJSON := filepath.Join(home, ".claude.json")
	proj := filepath.Join(home, "myproject")

	cfg := claudeCodeConfig{
		Projects: map[string]struct {
			MCPServers map[string]clientServerConfig `json:"mcpServers"`
		}{
			proj: {MCPServers: map[string]clientServerConfig{
				"custom": {Type: "stdio", Command: "node", Args: []string{"./server.js"}},
			}},
		},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writeJSON(t, claudeJSON, string(raw))

	out := scanClaudeCode(filepath.Join(home, "other-project"))
	if len(out) != 0 {
		t.Errorf("expected 0 entries for an unrelated cwd, got %+v", out)
	}
}
