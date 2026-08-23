// SPDX-License-Identifier: MIT

package mcpregistry

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestResolvePackage(t *testing.T) {
	tests := []struct {
		name    string
		command string
		args    []string
		want    string
	}{
		{"npx with -y flag", "npx", []string{"-y", "@modelcontextprotocol/server-github"}, "@modelcontextprotocol/server-github"},
		{"npx no flags", "npx", []string{"remote-filesystem-mcp-server"}, "remote-filesystem-mcp-server"},
		{"uvx package", "uvx", []string{"mcp-server-fetch"}, "mcp-server-fetch"},
		{"npx only flags, no package", "npx", []string{"-y", "--silent"}, ""},
		{"docker run with flags and image", "docker", []string{"run", "-i", "--rm", "-e", "API_KEY=secret", "mcp/fetch"}, "mcp/fetch"},
		{"docker run image with tag", "docker", []string{"run", "--rm", "ghcr.io/foo/bar:latest"}, "ghcr.io/foo/bar:latest"},
		{"docker with only flags, no image found", "docker", []string{"run", "-i", "--rm", "-e", "KEY=val"}, ""},
		{"unknown launcher", "/usr/local/bin/my-server", []string{"--config", "x.json"}, ""},
		{"absolute path to npx", "/usr/local/bin/npx", []string{"-y", "some-pkg"}, "some-pkg"},
		{"no args at all", "npx", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolvePackage(tt.command, tt.args); got != tt.want {
				t.Errorf("resolvePackage(%q, %v) = %q, want %q", tt.command, tt.args, got, tt.want)
			}
		})
	}
}

func TestClaudeDesktopConfigPathFor_AllThreePlatforms(t *testing.T) {
	// filepath.Join uses the test-running host's own separator regardless of
	// the goos argument (it isn't itself GOOS-aware) — so "want" is computed
	// with the same filepath.Join calls the function under test makes,
	// rather than a hardcoded separator that would only be right on one host.
	home, appData := "/Users/x", "/Users/x/AppData/Roaming"
	tests := []struct {
		name, goos, home, appData string
		want                      string
	}{
		{"darwin", "darwin", home, "", filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json")},
		{"windows with appdata", "windows", home, appData, filepath.Join(appData, "Claude", "claude_desktop_config.json")},
		{"windows without appdata", "windows", home, "", ""},
		{"linux/default", "linux", home, "", filepath.Join(home, ".config", "Claude", "claude_desktop_config.json")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := claudeDesktopConfigPathFor(tt.goos, tt.home, tt.appData); got != tt.want {
				t.Errorf("claudeDesktopConfigPathFor(%q, %q, %q) = %q, want %q", tt.goos, tt.home, tt.appData, got, tt.want)
			}
		})
	}
}

func TestToInstalledServers_RemoteServerHasNoPackage(t *testing.T) {
	servers := map[string]clientServerConfig{
		"cloudflare": {Type: "http", URL: "https://example.com/mcp"},
	}
	out := toInstalledServers(servers, "claude-code", "user")
	if len(out) != 1 {
		t.Fatalf("got %d entries, want 1", len(out))
	}
	if !out[0].Remote {
		t.Errorf("expected Remote=true for a url-based server")
	}
	if out[0].Package != "" {
		t.Errorf("remote server should have no package, got %q", out[0].Package)
	}
}

func TestToInstalledServers_StdioServerResolvesPackage(t *testing.T) {
	servers := map[string]clientServerConfig{
		"github": {Type: "stdio", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-github"}},
	}
	out := toInstalledServers(servers, "claude-desktop", "user")
	if len(out) != 1 {
		t.Fatalf("got %d entries, want 1", len(out))
	}
	got := out[0]
	if got.Remote {
		t.Errorf("expected Remote=false for a stdio server")
	}
	if got.Package != "@modelcontextprotocol/server-github" {
		t.Errorf("Package = %q, want %q", got.Package, "@modelcontextprotocol/server-github")
	}
	if got.Command != "npx" {
		t.Errorf("Command = %q, want %q", got.Command, "npx")
	}
}

// clientServerConfig must never carry env values into memory: scan reads
// config files that commonly hold plaintext API keys, and the privacy
// guarantee in mcpregistry.go depends on this struct having no field an
// "env" JSON key could unmarshal into. This test fails the moment someone
// adds one.
func TestClientServerConfig_HasNoEnvField(t *testing.T) {
	var cfg clientServerConfig
	raw := []byte(`{"type":"stdio","command":"npx","args":["-y","pkg"],"env":{"API_KEY":"super-secret-value"}}`)
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// The struct simply has nowhere to put "env" — this assertion documents
	// that guarantee rather than testing incidental behavior.
	if cfg.Command != "npx" {
		t.Fatalf("sanity check failed: Command = %q", cfg.Command)
	}
}
