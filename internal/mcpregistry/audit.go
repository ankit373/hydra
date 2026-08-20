// SPDX-License-Identifier: MIT

package mcpregistry

import (
	"os"
	"time"
)

// Status is the Phase 1 (identity-only) resolution of one installed server
// against the synced registry. There is no trust score yet — that's Phase 2.
type Status string

const (
	// StatusVerified means the server's resolved package matches a
	// namespace-verified entry in the official registry.
	StatusVerified Status = "verified"
	// StatusUnresolved means no match was found — a remote (URL-based)
	// server, a launcher this package couldn't resolve to a package
	// identifier, or a package genuinely absent from the registry (private,
	// custom, or simply not the one it claims to be). Not necessarily
	// malicious; it's exactly the category worth a second look.
	StatusUnresolved Status = "unresolved"
)

// AuditEntry is one installed server plus its Phase 1 resolution.
type AuditEntry struct {
	InstalledServer
	Status       Status `json:"status"`
	RegistryName string `json:"registry_name,omitempty"`
}

// AuditReport is the result of `hyctl mcp registry audit`.
type AuditReport struct {
	GeneratedAt  time.Time    `json:"generated_at"`
	RegistrySync time.Time    `json:"registry_synced_at,omitzero"` // zero if sync has never run
	Entries      []AuditEntry `json:"entries"`
}

// Audit scans this machine's MCP clients and resolves each installed server
// against the last-synced registry dataset. Works with no prior sync (every
// entry reports unresolved and RegistrySync is zero) — sync is what upgrades
// the report from "here's what's installed" to "here's what's verified".
func Audit(cwd string) (*AuditReport, error) {
	installed := Scan(cwd)

	report := &AuditReport{GeneratedAt: time.Now().UTC(), Entries: make([]AuditEntry, 0, len(installed))}

	cache, err := loadCache()
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	var index map[string]ServerRecord
	if cache != nil {
		report.RegistrySync = cache.FetchedAt
		index = buildPackageIndex(cache.Servers)
	}

	for _, s := range installed {
		entry := AuditEntry{InstalledServer: s, Status: StatusUnresolved}
		if !s.Remote && s.Package != "" {
			if match, ok := index[s.Package]; ok {
				entry.Status = StatusVerified
				entry.RegistryName = match.Name
			}
		}
		report.Entries = append(report.Entries, entry)
	}
	return report, nil
}

// buildPackageIndex maps a package identifier to the registry server that
// publishes it. Last-write-wins on a rare identifier collision — acceptable
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
