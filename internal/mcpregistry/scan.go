// SPDX-License-Identifier: MIT

package mcpregistry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// clientServerConfig mirrors one entry inside a client's mcpServers/servers
// map. There is deliberately no field for "env" or any other config value:
// these files commonly hold API keys and tokens in plaintext, and scan must
// never read, log, or transmit them. Adding an Env field here is a privacy
// regression — identity fields only (command/args/url), never values that
// could carry a secret.
type clientServerConfig struct {
	Type    string   `json:"type,omitempty"`
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	URL     string   `json:"url,omitempty"`
}

// Scan enumerates MCP servers configured across this machine's known
// clients (Claude Code, Claude Desktop, Cursor, Windsurf, VS Code).
// cwd scopes project-level lookups; pass "" to skip project-scoped sources.
// A missing config file for a client is not an error — most machines won't
// have every client installed.
func Scan(cwd string) []InstalledServer {
	var out []InstalledServer
	out = append(out, scanClaudeCode(cwd)...)
	out = append(out, scanClaudeDesktop()...)
	out = append(out, scanMCPServersFile(cursorGlobalConfigPath(), "cursor", "user")...)
	if cwd != "" {
		out = append(out, scanMCPServersFile(filepath.Join(cwd, ".cursor", "mcp.json"), "cursor", "project")...)
	}
	out = append(out, scanMCPServersFile(windsurfConfigPath(), "windsurf", "user")...)
	if cwd != "" {
		out = append(out, scanVSCodeFile(filepath.Join(cwd, ".vscode", "mcp.json"))...)
	}
	return out
}

// claudeCodeConfig mirrors the parts of ~/.claude.json this package reads.
type claudeCodeConfig struct {
	MCPServers map[string]clientServerConfig `json:"mcpServers"`
	Projects   map[string]struct {
		MCPServers map[string]clientServerConfig `json:"mcpServers"`
	} `json:"projects"`
}

func scanClaudeCode(cwd string) []InstalledServer {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		return nil
	}
	var cfg claudeCodeConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil
	}

	out := toInstalledServers(cfg.MCPServers, "claude-code", "user")
	if cwd != "" {
		if proj, ok := cfg.Projects[cwd]; ok {
			out = append(out, toInstalledServers(proj.MCPServers, "claude-code", "project")...)
		}
	}
	return out
}

func scanClaudeDesktop() []InstalledServer {
	return scanMCPServersFile(claudeDesktopConfigPath(), "claude-desktop", "user")
}

func claudeDesktopConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return claudeDesktopConfigPathFor(runtime.GOOS, home, os.Getenv("APPDATA"))
}

// claudeDesktopConfigPathFor is the pure, OS-parameterized core of
// claudeDesktopConfigPath — split out so all three platform branches are
// unit-testable on every CI runner, not just whichever OS happens to be
// running the test (goos/appData wouldn't otherwise vary within one job).
func claudeDesktopConfigPathFor(goos, home, appData string) string {
	switch goos {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json")
	case "windows":
		if appData == "" {
			return ""
		}
		return filepath.Join(appData, "Claude", "claude_desktop_config.json")
	default:
		return filepath.Join(home, ".config", "Claude", "claude_desktop_config.json")
	}
}

func cursorGlobalConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cursor", "mcp.json")
}

func windsurfConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codeium", "windsurf", "mcp_config.json")
}

// scanMCPServersFile reads a {"mcpServers": {name: config}} file, the shape
// shared by Claude Desktop, Cursor, and Windsurf.
func scanMCPServersFile(path, client, scope string) []InstalledServer {
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cfg struct {
		MCPServers map[string]clientServerConfig `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil
	}
	return toInstalledServers(cfg.MCPServers, client, scope)
}

// scanVSCodeFile reads VS Code's {"servers": {name: config}} shape — the one
// client here that uses "servers" instead of "mcpServers".
func scanVSCodeFile(path string) []InstalledServer {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cfg struct {
		Servers map[string]clientServerConfig `json:"servers"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil
	}
	return toInstalledServers(cfg.Servers, "vscode", "project")
}

func toInstalledServers(servers map[string]clientServerConfig, client, scope string) []InstalledServer {
	out := make([]InstalledServer, 0, len(servers))
	for name, cfg := range servers {
		if cfg.URL != "" {
			out = append(out, InstalledServer{Client: client, Scope: scope, Name: name, Remote: true})
			continue
		}
		out = append(out, InstalledServer{
			Client:  client,
			Scope:   scope,
			Name:    name,
			Command: filepath.Base(cfg.Command),
			Package: resolvePackage(cfg.Command, cfg.Args),
		})
	}
	return out
}

// dockerSubcommands are bare tokens that appear in a docker invocation but
// are never the image — excluded so a config with only flags and no real
// image argument doesn't misresolve one of these as if it were the image.
var dockerSubcommands = map[string]bool{"run": true, "exec": true, "start": true}

// resolvePackage makes a best-effort guess at the package identifier a
// stdio launcher command runs, so it can be matched against the registry's
// packages[].identifier. Deliberately conservative: an unresolved package
// (empty string) is safe — it just shows up as "unresolved" in the audit,
// not as a false claim about what the server is.
func resolvePackage(command string, args []string) string {
	switch filepath.Base(command) {
	case "npx", "uvx":
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				return a
			}
		}
	case "docker":
		// Best-effort: the image is usually the last bare token that isn't
		// a flag, doesn't look like an env-style KEY=VALUE pair passed to a
		// preceding -e/--env flag, and isn't the docker subcommand itself
		// ("run"/"exec"/"start" are bare tokens too — a config with only
		// flags and no real image argument would otherwise misresolve one
		// of these as if it were the image).
		for i := len(args) - 1; i >= 0; i-- {
			a := args[i]
			if strings.HasPrefix(a, "-") || strings.Contains(a, "=") || dockerSubcommands[a] {
				continue
			}
			return a
		}
	}
	return ""
}
