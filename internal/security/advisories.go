// SPDX-License-Identifier: MIT

package security

import (
	"fmt"
	"sort"
	"strings"
)

// AdvisoryState says what happened when advisories were looked up, because a
// server with none found and a server nobody asked about must never render the
// same. The zero value is "nobody asked", which is the default: the lookup is
// the one outbound request this report can make and is opt-in.
type AdvisoryState string

const (
	AdvisoriesNotRequested   AdvisoryState = ""
	AdvisoriesUnknownVersion AdvisoryState = "unknown-version"
	AdvisoriesNotQueryable   AdvisoryState = "not-queryable"
	AdvisoriesFailed         AdvisoryState = "failed"
	AdvisoriesChecked        AdvisoryState = "checked"
)

// Advisory is one published advisory against a server's version. It mirrors
// internal/osv's type rather than importing it, so this package keeps the
// no-network invariant Build rests on.
type Advisory struct {
	ID      string `json:"id"`
	CVE     string `json:"cve,omitempty"`
	Summary string `json:"summary,omitempty"`
	// FixedIn is empty when no fix has shipped. That is the distinction the
	// whole check turns on: an advisory with a fix is an upgrade, and one
	// without cannot be answered by upgrading at all.
	FixedIn string `json:"fixedIn,omitempty"`
}

// advisoryCheck reports what is published against the local model servers.
//
// Split by whether a fix exists, because counting them together is useless in
// practice: a fully current Ollama still carries nine advisories with no fix
// upstream, so a single total would read the same on a patched server as on an
// abandoned one and teach the reader to ignore it (#925).
func advisoryCheck(servers []LocalServer) Check {
	c := Check{Name: "Local server advisories"}
	if len(servers) == 0 {
		c.Status = "not evaluated"
		c.Detail = "no local model server was discovered on this machine"
		return c
	}

	var fixable, unfixed []string
	var checked, skipped int
	for _, s := range servers {
		switch s.AdvisoryState {
		case AdvisoriesNotRequested:
			continue
		case AdvisoriesChecked:
			checked++
		default:
			skipped++
			continue
		}
		for _, a := range s.Advisories {
			if a.FixedIn != "" {
				fixable = append(fixable, fmt.Sprintf("%s in %s %s, fixed in %s",
					advisoryName(a), s.Kind, s.Version, a.FixedIn))
				continue
			}
			unfixed = append(unfixed, advisoryName(a))
		}
	}

	if checked == 0 && skipped == 0 {
		c.Status = "not checked"
		c.Detail = "advisories are not looked up by default, since it is the one thing here that " +
			"leaves the machine; `hyctl security --advisories` asks OSV about the versions found"
		return c
	}

	sort.Strings(fixable)
	sort.Strings(unfixed)

	switch {
	case len(fixable) > 0:
		c.Status = fmt.Sprintf("%d fixable", len(fixable))
		c.Detail = "upgrade answers these: " + strings.Join(capped(fixable, 3), "; ")
		if len(unfixed) > 0 {
			c.Detail += fmt.Sprintf(". A further %d have no fix upstream", len(unfixed))
		}
	case len(unfixed) > 0:
		c.Status = fmt.Sprintf("%d unfixed upstream", len(unfixed))
		c.Detail = fmt.Sprintf("no upgrade answers these, so the mitigation is not reaching them: "+
			"%s. Keeping the server on loopback is what the exposure check above is about",
			strings.Join(capped(unfixed, 3), ", "))
	case checked > 0:
		c.Status = "no known advisories"
		c.Detail = fmt.Sprintf("%d server version(s) carried nothing OSV has published against them", checked)
	default:
		c.Status = "not checked"
	}
	if skipped > 0 {
		c.Detail += fmt.Sprintf(". %s", strings.Join(skippedReasons(servers), "; "))
	}
	return c
}

// advisoryName prefers the CVE, which a reader recognises, over OSV's own GHSA
// or GO id, which most people cannot place.
func advisoryName(a Advisory) string {
	if a.CVE != "" {
		return a.CVE
	}
	return a.ID
}

// skippedReasons says which servers were not asked about and why, so a partial
// answer is never read as a whole one.
func skippedReasons(servers []LocalServer) []string {
	var out []string
	for _, s := range servers {
		switch s.AdvisoryState {
		case AdvisoriesUnknownVersion:
			out = append(out, fmt.Sprintf("%s publishes no version, so it could not be asked about", s.Kind))
		case AdvisoriesNotQueryable:
			out = append(out, fmt.Sprintf("%s has no package OSV tracks by version", s.Kind))
		case AdvisoriesFailed:
			out = append(out, fmt.Sprintf("the lookup for %s did not complete, which is unchecked and not clean", s.Kind))
		}
	}
	sort.Strings(out)
	return out
}

func capped(items []string, n int) []string {
	if len(items) <= n {
		return items
	}
	return append(append([]string{}, items[:n]...), fmt.Sprintf("and %d more", len(items)-n))
}
