// SPDX-License-Identifier: MIT

package trust

import (
	"path/filepath"
	"strconv"
	"strings"
)

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

// DomainForFile derives the calibration domain for work on a file: its
// extension, lowercased, or DefaultDomain when it has none.
//
// This has to be one function. editor and review each derived it inline from
// filepath.Ext while `hyctl dispatch --confidence` looked up whatever --domain
// said, so a session that edited Go files filled the "go" cell while every
// confidence run read "default" and found nothing, refusing with ErrNoEvidence
// however much history had accumulated (#785).
//
// Lowercased because a map key is case-sensitive and ".Go" and ".go" are the
// same language; the extension alone rather than anything richer because it is
// what both writers already recorded, and changing the shape would orphan the
// history that exists.
func DomainForFile(path string) string {
	ext := strings.TrimPrefix(filepath.Ext(strings.TrimSpace(path)), ".")
	return Domain(strings.ToLower(ext))
}

// unreadableSourcePrefixes are the source-key shapes the router never looks up.
// SPRT keys calibration on a head's own ID (see swarm's trust.Source), so a row
// filed under "model:<name>" is written and never read.
var unreadableSourcePrefixes = []string{"model:", "head:", "provider:"}

// UnreadableSourceKey reports whether a calibration source key is one the SPRT
// ensemble can never match, and why.
//
// The docs told people to record `--source model:claude-sonnet` while the
// router looks up the raw head ID from `hyctl probe`, so those rows accumulated
// somewhere nothing reads (#785). Verifier keys are exempt: `hyctl oracle
// verify` both writes and reads "verifier:<cmd>", so that prefix is coherent.
func UnreadableSourceKey(source string) (reason string, unreadable bool) {
	lower := strings.ToLower(strings.TrimSpace(source))
	for _, p := range unreadableSourcePrefixes {
		if strings.HasPrefix(lower, p) {
			return "the ensemble keys calibration on a head's own ID, not a " +
				strconv.Quote(p) + " prefix, so this row will never be read", true
		}
	}
	return "", false
}
