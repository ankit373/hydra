// SPDX-License-Identifier: MIT

package mcpregistry

import (
	"encoding/json"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// DirectoryEntry is one server's public-facing record for the Phase 3
// static export — every field a reader needs to see the score with its
// reasoning, never a bare number with nothing behind it.
type DirectoryEntry struct {
	Name           string         `json:"name"`
	RepositoryURL  string         `json:"repository_url,omitempty"`
	LifecycleState LifecycleState `json:"lifecycle_state"`
	Score          Score          `json:"score"`
	LastCheckedAt  time.Time      `json:"last_checked_at"`
}

// Directory gathers every server this machine has ever audited (via
// `hyctl mcp registry audit`) into the display shape both the static export
// and `hyctl mcp registry list` use — one source of truth for "what has
// this machine scored," sorted by name. Deliberately bounded to what's
// actually been scored, not an attempt to cover the full synced registry:
// the design doc's own non-goals rule out a fifth undifferentiated
// full-corpus index, and eagerly scoring tens of thousands of servers would
// exhaust GitHub's unauthenticated rate limit before a single run finished.
func Directory() ([]DirectoryEntry, error) {
	states, err := LoadStates()
	if err != nil {
		return nil, err
	}

	cache, err := loadCache()
	var byName map[string]ServerRecord
	if err == nil && cache != nil {
		byName = make(map[string]ServerRecord, len(cache.Servers))
		for _, s := range cache.Servers {
			byName[s.Name] = s
		}
	}

	entries := make([]DirectoryEntry, 0, len(states))
	for name, st := range states {
		entry := DirectoryEntry{
			Name:           name,
			LifecycleState: st.State,
			Score:          st.LastScore,
			LastCheckedAt:  st.StateChangedAt,
		}
		if srv, ok := byName[name]; ok {
			entry.RepositoryURL = srv.Repository.URL
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

// ExportDirectory renders Directory() into a self-contained static site
// under outDir: index.json (the raw data, for anyone building their own
// frontend against it) and index.html (a plain, dependency-free page).
// Returns the number of entries written.
//
// This produces static files only — it does not publish, host, or deploy
// anything anywhere. Where these files end up (if anywhere) is a decision
// for whoever runs this, not something this function makes on its own.
func ExportDirectory(outDir string) (int, error) {
	entries, err := Directory()
	if err != nil {
		return 0, err
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return 0, err
	}

	rawJSON, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(filepath.Join(outDir, "index.json"), rawJSON, 0o644); err != nil {
		return 0, err
	}

	f, err := os.Create(filepath.Join(outDir, "index.html"))
	if err != nil {
		return 0, err
	}
	defer f.Close()
	// html/template, not string concatenation: every field rendered below —
	// server name, signal detail — originates from the official registry,
	// which by its own moderation policy applies "minimal-to-no" review
	// (§2 of the design doc). A malicious entry's name or a signal's Detail
	// string is untrusted input as far as this generator is concerned, and
	// auto-escaping is what stops it from becoming markup in the exported
	// page instead of text.
	if err := directoryTemplate.Execute(f, entries); err != nil {
		return 0, err
	}

	return len(entries), nil
}

var directoryTemplate = template.Must(template.New("directory").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Hydra MCP Trust Directory (local export)</title>
<style>
body { font-family: system-ui, sans-serif; max-width: 960px; margin: 2rem auto; padding: 0 1rem; }
table { width: 100%; border-collapse: collapse; }
th, td { text-align: left; padding: 0.5rem; border-bottom: 1px solid #ddd; vertical-align: top; }
.state-trusted { color: #0a7a2f; }
.state-provisional { color: #8a6d00; }
.state-flagged, .state-quarantined, .state-delisted { color: #b00020; }
.signals { font-size: 0.85em; color: #555; }
</style>
</head>
<body>
<h1>Hydra MCP Trust Directory</h1>
<p>Locally exported — servers this machine has audited via <code>hyctl mcp registry audit</code>, not the full official registry.</p>
<table>
<thead><tr><th>Server</th><th>State</th><th>Score</th><th>Signals</th><th>Last checked</th></tr></thead>
<tbody>
{{range .}}
<tr>
<td>{{.Name}}{{if .RepositoryURL}}<br><a href="{{.RepositoryURL}}">{{.RepositoryURL}}</a>{{end}}</td>
<td class="state-{{.LifecycleState}}">{{.LifecycleState}}</td>
<td>{{if eq .Score.Confidence "insufficient_evidence"}}insufficient evidence{{else}}{{printf "%.0f" .Score.Overall}}/100 ({{.Score.Confidence}}){{end}}</td>
<td class="signals">
{{range .Score.SecurityImplementation.Signals}}{{if .Available}}{{.Detail}}<br>{{end}}{{end}}
{{range .Score.RepositoryHealth.Signals}}{{if .Available}}{{.Detail}}<br>{{end}}{{end}}
{{range .Score.OperationalSecurity.Signals}}{{if .Available}}{{.Detail}}<br>{{end}}{{end}}
</td>
<td>{{.LastCheckedAt.Format "2006-01-02"}}</td>
</tr>
{{end}}
</tbody>
</table>
</body>
</html>
`))
