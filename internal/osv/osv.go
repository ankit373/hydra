// SPDX-License-Identifier: MIT

// Package osv queries OSV.dev for advisories published against a package.
//
// One client rather than two: internal/mcpregistry scores MCP servers with it,
// and internal/probe asks the same question about the model servers Hydra
// routes to (#925).
package osv

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// QueryURL is OSV.dev's query endpoint, a var so a test can point it at a fake.
var QueryURL = "https://api.osv.dev/v1/query"

var client = &http.Client{Timeout: 15 * time.Second}

// Advisory is one published advisory against the queried package.
type Advisory struct {
	ID      string `json:"id"`
	CVE     string `json:"cve,omitempty"`
	Summary string `json:"summary,omitempty"`
	// FixedIn is the version that fixes it, empty when no fix has shipped.
	// The distinction is the whole point: an advisory with a fix is an upgrade
	// and an advisory without one cannot be answered by upgrading at all.
	FixedIn string `json:"fixedIn,omitempty"`
}

// Fixed reports whether upgrading is an answer to this advisory.
func (a Advisory) Fixed() bool { return a.FixedIn != "" }

type rawAdvisory struct {
	ID       string   `json:"id"`
	Summary  string   `json:"summary"`
	Aliases  []string `json:"aliases"`
	Affected []struct {
		Package struct {
			Name      string `json:"name"`
			Ecosystem string `json:"ecosystem"`
		} `json:"package"`
		Ranges []struct {
			Events []map[string]string `json:"events"`
		} `json:"ranges"`
	} `json:"affected"`
}

type rawResponse struct {
	Vulns []rawAdvisory `json:"vulns"`
}

// Query asks OSV about one package. An empty version asks about the package as
// a whole; a version asks only for advisories that version is subject to, which
// is what makes the answer about the server actually running.
func Query(ctx context.Context, ecosystem, name, version string) ([]Advisory, error) {
	return QueryAt(ctx, QueryURL, ecosystem, name, version)
}

// QueryAt is Query against a named endpoint, for callers holding their own URL.
func QueryAt(ctx context.Context, url, ecosystem, name, version string) ([]Advisory, error) {
	if ecosystem == "" || name == "" {
		return nil, fmt.Errorf("osv: ecosystem and name are both required")
	}
	pkg := map[string]string{"name": name, "ecosystem": ecosystem}
	payload := map[string]any{"package": pkg}
	if version != "" {
		payload["version"] = version
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("osv: %s", resp.Status)
	}

	var out rawResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	advisories := make([]Advisory, 0, len(out.Vulns))
	for _, v := range out.Vulns {
		advisories = append(advisories, Advisory{
			ID:      v.ID,
			CVE:     cveAlias(v.Aliases),
			Summary: strings.TrimSpace(v.Summary),
			FixedIn: fixedIn(v, name),
		})
	}
	return advisories, nil
}

// cveAlias picks the CVE out of the alias list, the id a reader recognises.
// OSV's own id is a GHSA or GO identifier, which most people cannot place.
func cveAlias(aliases []string) string {
	for _, a := range aliases {
		if strings.HasPrefix(a, "CVE-") {
			return a
		}
	}
	return ""
}

// fixedIn returns the version that fixes this advisory for the package asked
// about, or "" when no fix has shipped. Ranges for other packages in the same
// advisory are skipped: a fix in a dependency is not a fix here.
func fixedIn(v rawAdvisory, name string) string {
	for _, a := range v.Affected {
		if a.Package.Name != name {
			continue
		}
		for _, r := range a.Ranges {
			for _, e := range r.Events {
				if f := strings.TrimSpace(e["fixed"]); f != "" {
					return f
				}
			}
		}
	}
	return ""
}
