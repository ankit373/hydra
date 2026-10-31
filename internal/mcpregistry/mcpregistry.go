// SPDX-License-Identifier: MIT

// Package mcpregistry is the Phase 1 CLI wedge of Hydra's MCP registry:
// identity-only sync of the official MCP registry, a scan of what's actually
// installed across this machine's MCP clients, and an audit that resolves one
// against the other. No trust scoring yet, that's Phase 2.
package mcpregistry

import (
	"path/filepath"
	"time"

	"github.com/ankit373/hydra/internal/config"
)

// EnvVar is one environment variable a package declares it needs, as
// published in the registry's server.json. Identity/shape only, a
// variable's name and whether it's secret-shaped, never a value, this is
// registry metadata about what the package asks for, not a real config
// file, so it carries none of the privacy risk scan.go's clientServerConfig
// deliberately avoids.
type EnvVar struct {
	Name     string `json:"name"`
	IsSecret bool   `json:"isSecret"`
}

// Package is one distribution channel a server ships through (npm, pypi,
// docker, ...), as published in the registry's server.json.
type Package struct {
	RegistryType         string   `json:"registryType"`
	Identifier           string   `json:"identifier"`
	EnvironmentVariables []EnvVar `json:"environmentVariables"`
}

// Repository is the source repo a server's registry entry points at.
type Repository struct {
	URL string `json:"url"`
}

// RemoteHeader is one HTTP header a remote (URL-based) server declares it
// needs, the registry's mechanism for a server to say "call me with this
// auth header". IsSecret is what marks it as a credential rather than an
// arbitrary header.
type RemoteHeader struct {
	Name     string `json:"name"`
	IsSecret bool   `json:"isSecret"`
}

// Remote is one network endpoint a server exposes.
type Remote struct {
	Type    string         `json:"type"`
	URL     string         `json:"url"`
	Headers []RemoteHeader `json:"headers"`
}

// ServerRecord is one server as published by the official MCP registry.
// Presence in the registry under a reverse-DNS name (e.g. "io.github.x/y")
// already implies namespace ownership was verified at publish time (GitHub
// OIDC or DNS challenge), there is no separate "verified" flag to check.
type ServerRecord struct {
	Name       string     `json:"name"`
	Version    string     `json:"version"`
	Repository Repository `json:"repository"`
	Packages   []Package  `json:"packages"`
	Remotes    []Remote   `json:"remotes"`
}

// InstalledServer is one MCP server found configured on this machine.
// Identity only, by construction: the parser this feeds from never declares
// a field for env vars or other config values, so a secret cannot end up
// here even by accident, see clientServerConfig in scan.go.
type InstalledServer struct {
	Client  string `json:"client"`            // "claude-code", "claude-desktop", "cursor", "windsurf"
	Scope   string `json:"scope"`             // "user" or "project"
	Name    string `json:"name"`              // the config key / server name
	Command string `json:"command,omitempty"` // launcher binary (npx, uvx, docker, ...), never full args
	Package string `json:"package,omitempty"` // best-effort resolved package identifier
	Remote  bool   `json:"remote"`            // true for url-based (http/sse) servers, no package to resolve
}

func cachePath() string {
	return filepath.Join(config.Dir(), "mcp_registry_cache.json")
}

// registryCache is the on-disk shape written by Sync and read by Audit.
type registryCache struct {
	FetchedAt time.Time      `json:"fetched_at"`
	Servers   []ServerRecord `json:"servers"`
}
