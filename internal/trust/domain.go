// SPDX-License-Identifier: MIT

package trust

import "strings"

// DefaultDomain is the calibration domain used when a caller names none.
const DefaultDomain = "default"

// Domain normalizes a caller-supplied calibration domain.
//
// The rule used to live privately in swarm.Run, swarm.SPRT and logTrustRun, so
// a run and its log entry both recorded "default" while the error path saw the
// raw "" and told the user to run `--domain ` with no value (#732). One copy,
// so no caller can be the one that missed it.
//
// Space is trimmed because a domain is a map key: " go" and "go" would be two
// calibration histories that look identical in every report.
func Domain(s string) string {
	if s = strings.TrimSpace(s); s != "" {
		return s
	}
	return DefaultDomain
}
