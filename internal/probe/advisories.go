// SPDX-License-Identifier: MIT

package probe

import (
	"context"
	"sync"

	"github.com/ankit373/hydra/internal/osv"
	"github.com/ankit373/hydra/internal/security"
)

// osvCoordinates maps a local server kind to how OSV knows it. A kind absent
// here is not queryable: LM Studio is closed source with no package to ask
// about, and llama.cpp publishes a build id (`b4567-abc1234`) rather than a
// version OSV can order.
var osvCoordinates = map[string]struct{ ecosystem, name string }{
	"ollama":  {"Go", "github.com/ollama/ollama"},
	"litellm": {"PyPI", "litellm"},
}

// LookupAdvisories asks OSV what is published against each scanned server's
// version. It is the only outbound request Hydra's security report can make,
// so it is opt-in at the call site rather than part of the scan (#925).
func LookupAdvisories(ctx context.Context, servers []security.LocalServer) []security.LocalServer {
	out := make([]security.LocalServer, len(servers))
	copy(out, servers)

	var wg sync.WaitGroup
	for i := range out {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out[i] = lookupOne(ctx, out[i])
		}(i)
	}
	wg.Wait()
	return out
}

func lookupOne(ctx context.Context, s security.LocalServer) security.LocalServer {
	coord, queryable := osvCoordinates[s.Kind]
	if !queryable {
		s.AdvisoryState = security.AdvisoriesNotQueryable
		return s
	}
	// Asking without a version would return every advisory ever filed against
	// the package, which says nothing about the server actually running.
	if s.Version == "" {
		s.AdvisoryState = security.AdvisoriesUnknownVersion
		return s
	}

	found, err := osv.Query(ctx, coord.ecosystem, coord.name, s.Version)
	if err != nil {
		s.AdvisoryState = security.AdvisoriesFailed
		return s
	}
	s.AdvisoryState = security.AdvisoriesChecked
	for _, a := range found {
		s.Advisories = append(s.Advisories, security.Advisory{
			ID: a.ID, CVE: a.CVE, Summary: a.Summary, FixedIn: a.FixedIn,
		})
	}
	return s
}
