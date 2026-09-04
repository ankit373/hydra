// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"os"
	"time"

	"github.com/ankit373/hydra/internal/mcpregistry"
)

// AuditTimeout bounds a registry audit. Scoring a resolved server reaches
// OSV.dev and GitHub, so an audit on a machine with a synced cache does
// network work; the view must not hang on it.
const AuditTimeout = 25 * time.Second

// SyncTimeout bounds pulling the official registry, which is paginated.
const SyncTimeout = 90 * time.Second

// MCPServer is one MCP server installed on this machine.
//
// Identity only, by construction: mcpregistry.Scan never reads env or secret
// values out of a client config, and nothing here reintroduces them.
type MCPServer struct {
	Name    string `json:"name"`
	Client  string `json:"client"`
	Scope   string `json:"scope"`
	Command string `json:"command,omitempty"`
	Package string `json:"package,omitempty"`
	Remote  bool   `json:"remote"`

	// Status is "verified" or "unresolved". Unresolved means the registry has
	// nothing under this identity — which with no sync is every server.
	Status string `json:"status"`
	State  string `json:"state,omitempty"`

	// Score is 0-100 and is meaningless unless Confidence says otherwise.
	// Scored is false when no score exists at all, so the view can say
	// "not scored" rather than draw a zero.
	Scored     bool    `json:"scored"`
	Score      float64 `json:"score"`
	Confidence string  `json:"confidence,omitempty"`

	// NearestMatch is the closest known identifier for an unresolved server,
	// with its edit distance — the typosquat signal. Empty when not computed.
	NearestMatch string `json:"nearestMatch,omitempty"`
	NearestDist  int    `json:"nearestDist,omitempty"`
}

// MCPPanel is what this machine can let an agent call, and what is known about it.
type MCPPanel struct {
	Servers []MCPServer `json:"servers"`

	// Synced is when the official registry was last pulled, RFC3339, empty if
	// never. Load-bearing: with no sync every server reads unresolved, and a
	// list of unresolved servers is not a finding about those servers.
	Synced string `json:"synced,omitempty"`

	// Scanned is false when the audit could not run at all, so an empty list
	// does not read as "nothing installed".
	Scanned bool   `json:"scanned"`
	Error   string `json:"error,omitempty"`
}

// GetMCPServers audits the MCP servers installed on this machine.
//
// The registry has shipped since #588 with no desktop surface at all, so the
// app could tell you an agent touched a tool and nothing about whether the
// server behind it was ever safe to run with your credentials.
func (a *API) GetMCPServers() MCPPanel {
	out := MCPPanel{Servers: []MCPServer{}}

	cwd, err := os.Getwd()
	if err != nil {
		out.Error = err.Error()
		return out
	}

	ctx, cancel := context.WithTimeout(context.Background(), AuditTimeout)
	defer cancel()

	rep, err := mcpregistry.Audit(ctx, cwd)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.Scanned = true
	if !rep.RegistrySync.IsZero() {
		out.Synced = rep.RegistrySync.UTC().Format(time.RFC3339)
	}

	for _, e := range rep.Entries {
		s := MCPServer{
			Name: e.Name, Client: e.Client, Scope: e.Scope,
			Command: e.Command, Package: e.Package, Remote: e.Remote,
			Status: string(e.Status), State: string(e.LifecycleState),
			NearestMatch: e.NearestMatch, NearestDist: e.NearestDist,
		}
		if e.Score != nil {
			s.Scored = true
			s.Score = e.Score.Overall
			s.Confidence = string(e.Score.Confidence)
		}
		out.Servers = append(out.Servers, s)
	}
	return out
}

// SyncMCPRegistry pulls the official registry so an audit can resolve
// identities against it.
//
// User-triggered rather than automatic: it is a paginated network fetch that
// writes a cache, the same shape as `hyctl pricing refresh`. Offering it here
// is what keeps the view from telling a GUI user to go run a CLI command
// (#452) — without a sync, every server reads unresolved forever.
func (a *API) SyncMCPRegistry() MCPSyncResult {
	ctx, cancel := context.WithTimeout(context.Background(), SyncTimeout)
	defer cancel()

	n, err := mcpregistry.Sync(ctx, nil)
	if err != nil {
		return MCPSyncResult{Error: err.Error()}
	}
	return MCPSyncResult{Servers: n}
}

// MCPSyncResult is how many server records the sync stored, or why it could not.
type MCPSyncResult struct {
	Servers int    `json:"servers"`
	Error   string `json:"error,omitempty"`
}
