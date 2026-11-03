// SPDX-License-Identifier: MIT

package mcpregistry

import (
	"context"
	"os"
	"strings"
	"time"
)

// Status is the Phase 1 (identity-only) resolution of one installed server
// against the synced registry. There is no trust score yet, that's Phase 2.
type Status string

const (
	// StatusVerified means the server's resolved package matches a
	// namespace-verified entry in the official registry.
	StatusVerified Status = "verified"
	// StatusUnresolved means no match was found, a remote (URL-based)
	// server, a launcher this package couldn't resolve to a package
	// identifier, or a package genuinely absent from the registry (private,
	// custom, or simply not the one it claims to be). Not necessarily
	// malicious; it's exactly the category worth a second look.
	StatusUnresolved Status = "unresolved"
)

// AuditEntry is one installed server plus its resolution, score (Phase 2,
// resolved entries only), and lifecycle state.
type AuditEntry struct {
	InstalledServer
	Status         Status         `json:"status"`
	RegistryName   string         `json:"registry_name,omitempty"`
	NearestMatch   string         `json:"nearest_match,omitempty"`    // unresolved entries only: closest known identifier
	NearestDist    int            `json:"nearest_distance,omitempty"` // edit distance to NearestMatch, -1 if not computed
	Score          *Score         `json:"score,omitempty"`
	LifecycleState LifecycleState `json:"lifecycle_state,omitempty"`
	// ToolsChecked is false when no tools/list snapshot exists, so silence is never read as clean.
	ToolsChecked bool                 `json:"tools_checked"`
	ToolFindings []DescriptionFinding `json:"tool_findings,omitempty"`
	ToolDrift    *ToolDrift           `json:"tool_drift,omitempty"`
}

// AuditReport is the result of `hyctl mcp registry audit`.
type AuditReport struct {
	GeneratedAt  time.Time    `json:"generated_at"`
	RegistrySync time.Time    `json:"registry_synced_at,omitzero"` // zero if sync has never run
	Entries      []AuditEntry `json:"entries"`
}

// Audit scans this machine's MCP clients and resolves each installed server
// against the last-synced registry dataset. Works with no prior sync (every
// entry reports unresolved and RegistrySync is zero), sync is what upgrades
// the report from "here's what's installed" to "here's what's verified".
// Resolved entries additionally get a Phase 2 trust score and lifecycle
// state, persisted across runs so a later version bump can be detected.
func Audit(ctx context.Context, cwd string) (*AuditReport, error) {
	installed := Scan(cwd)

	report := &AuditReport{GeneratedAt: time.Now().UTC(), Entries: make([]AuditEntry, 0, len(installed))}

	cache, err := loadCache()
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	var index map[string]ServerRecord
	var corpus []ServerRecord
	if cache != nil {
		report.RegistrySync = cache.FetchedAt
		index = buildPackageIndex(cache.Servers)
		corpus = cache.Servers
	}

	states, err := LoadStates()
	if err != nil {
		return nil, err
	}
	aliases, err := LoadAliases()
	if err != nil {
		return nil, err
	}
	// Lifecycle state is per registry server, so one package under two config names is
	// advanced once a run. Diffing both snapshots in turn would drift every run.
	type resolved struct {
		srv   ServerRecord
		score Score
		tools map[string]Tool
	}
	byRegistry := make(map[string]*resolved)

	for _, s := range installed {
		entry := AuditEntry{InstalledServer: s, Status: StatusUnresolved, NearestDist: -1}
		tools, toolsErr := loadToolSnapshot(s.Name)
		if toolsErr != nil {
			return nil, toolsErr
		}
		if tools != nil {
			entry.ToolsChecked = true
			entry.ToolFindings = CheckDescriptions(tools)
		}
		if !s.Remote && s.Package != "" {
			if match, ok := index[s.Package]; ok {
				entry.Status = StatusVerified
				entry.RegistryName = match.Name
				r, seen := byRegistry[match.Name]
				if !seen {
					r = &resolved{srv: match, score: ComputeScore(ctx, match, corpus)}
					byRegistry[match.Name] = r
				}
				// The first snapshot in scan order wins, so the choice is the same every run.
				if r.tools == nil {
					r.tools = tools
				}
				entry.Score = &r.score
			} else if corpus != nil {
				nearest, dist := NearestIdentifier(s.Package, corpus)
				entry.NearestMatch, entry.NearestDist = nearest, dist
			}
		}
		aliases[strings.ToLower(s.Name)] = aliasEntry{RegistryName: entry.RegistryName, Status: entry.Status}
		report.Entries = append(report.Entries, entry)
	}

	now := time.Now().UTC()
	for name, r := range byRegistry {
		var prevPtr *ServerState
		if prev, ok := states[name]; ok {
			prevPtr = &prev
		}
		states[name] = Advance(prevPtr, r.srv, r.score, r.tools, now)
	}
	for i := range report.Entries {
		entry := &report.Entries[i]
		if _, ok := byRegistry[entry.RegistryName]; ok {
			st := states[entry.RegistryName]
			entry.LifecycleState, entry.ToolDrift = st.State, st.ToolDrift
		}
	}

	if len(byRegistry) > 0 {
		if err := SaveStates(states); err != nil {
			return nil, err
		}
	}
	if len(installed) > 0 {
		if err := SaveAliases(aliases); err != nil {
			return nil, err
		}
	}
	return report, nil
}

// buildPackageIndex maps a package identifier to the registry server that
// publishes it. Last-write-wins on a rare identifier collision, acceptable
// for Phase 1's identity resolution, which isn't a security decision on its
// own (see StatusUnresolved's doc comment).
func buildPackageIndex(servers []ServerRecord) map[string]ServerRecord {
	idx := make(map[string]ServerRecord)
	for _, srv := range servers {
		for _, pkg := range srv.Packages {
			if pkg.Identifier != "" {
				idx[pkg.Identifier] = srv
			}
		}
	}
	return idx
}
