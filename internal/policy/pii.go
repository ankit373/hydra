// SPDX-License-Identifier: MIT

package policy

import (
	"regexp"
	"strconv"
	"strings"
)

// PII detection decides whether a prompt is forced to local-only routing, so the
// two failure directions are not symmetric: a false negative sends a secret to a
// cloud provider, while a false positive only routes work to a local head. The
// detectors below lean toward detection wherever the ambiguity is real, and the
// few places that trade detection away say so explicitly.
//
// This is still a heuristic; replace with Presidio via sidecar for
// production-grade detection.

// detector is a pattern plus an optional validator.
//
// Go's regexp is RE2, which has no lookahead or lookbehind, so conditions like
// "an IPv4 address unless it is a version string" cannot be written as a
// pattern at all — they need code that sees the match in context (#169).
type detector struct {
	name string
	re   *regexp.Regexp
	// valid narrows a raw hit. nil means every hit counts. It receives the
	// whole prompt plus the match bounds so it can inspect surrounding text.
	valid func(prompt string, loc []int) bool
}

var detectors = []detector{
	// ── Secrets with unmistakable prefixes ───────────────────────────────────
	// These are the highest-confidence signals available and need no validation:
	// nothing else looks like them. None of them were detected before (#169).
	{name: "pem private key", re: regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY-----`)},
	{name: "aws access key id", re: regexp.MustCompile(`\b(?:A3T[A-Z0-9]|AKIA|ASIA|ABIA|ACCA)[0-9A-Z]{16}\b`)},
	{name: "github token", re: regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`)},
	{name: "slack token", re: regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`)},
	{name: "openai key", re: regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`)},
	{name: "google api key", re: regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{name: "jwt", re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]+`)},
	{name: "auth header", re: regexp.MustCompile(`(?i)\bauthorization\s*:\s*(?:bearer|basic|token)\s+\S+`)},

	// ── Personal identifiers ─────────────────────────────────────────────────
	{name: "email", re: regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)},

	// A separator is required. Nine bare digits are indistinguishable from an
	// order number, invoice id or primary key, and treating every such run as an
	// SSN forced local-only routing on ordinary queries like "look up order
	// 123456789". This does trade away detection of a bare, unformatted SSN —
	// a real loss, accepted because the formatted spelling is overwhelmingly
	// more common in text and the unformatted one is unknowable in isolation.
	{name: "ssn", re: regexp.MustCompile(`\b\d{3}[-.\s]\d{2}[-.\s]\d{4}\b`)},

	// 13–19 digits, optionally grouped, that pass Luhn. Luhn rejects ~90% of
	// random digit runs, which is what keeps long ids from reading as cards.
	{name: "credit card", re: regexp.MustCompile(`\b\d{4}[- ]?\d{4}[- ]?\d{4}[- ]?\d{1,7}\b`), valid: validLuhn},

	// ── Context-dependent ────────────────────────────────────────────────────
	{name: "credentials", re: regexp.MustCompile(`(?i)\b(?:password|passwd|secret|api[-_]?key|access[-_]?key|auth[-_]?token|token|private[-_]?key)\s*[:=]\s*("[^"]*"|'[^']*'|\S+)`), valid: validCredentialValue},
	{name: "ip address", re: regexp.MustCompile(`\b(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\b`), valid: validPublicIP},
}

// ContainsPII reports whether the prompt likely contains sensitive data.
func ContainsPII(req Request) bool {
	for _, d := range detectors {
		for _, loc := range d.re.FindAllStringSubmatchIndex(req.Prompt, -1) {
			if d.valid == nil || d.valid(req.Prompt, loc) {
				return true
			}
		}
	}
	return false
}

// validLuhn checks the card checksum. Without it, any 16-digit identifier —
// order numbers, trace ids — forced local-only routing.
func validLuhn(prompt string, loc []int) bool {
	digits := make([]int, 0, 19)
	for _, r := range prompt[loc[0]:loc[1]] {
		if r >= '0' && r <= '9' {
			digits = append(digits, int(r-'0'))
		}
	}
	if len(digits) < 13 || len(digits) > 19 {
		return false
	}
	sum, double := 0, false
	for i := len(digits) - 1; i >= 0; i-- {
		d := digits[i]
		if double {
			if d *= 2; d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}

// minCredentialValueLen is the shortest assignment value treated as a secret.
// "token: 4096" is a buffer size, not a credential; "password: hunter2" is one.
const minCredentialValueLen = 6

// validCredentialValue rejects assignments whose value cannot plausibly be a
// secret. The keyword alone is not enough: `token`, `secret` and `key` are all
// ordinary config names whose values are frequently small integers (#169).
func validCredentialValue(prompt string, loc []int) bool {
	if len(loc) < 4 || loc[2] < 0 {
		return false
	}
	v := strings.Trim(prompt[loc[2]:loc[3]], `"'`)
	if len(v) < minCredentialValueLen {
		return false
	}
	// A purely numeric value is a port, size, timeout or count. Real secrets
	// are effectively never all-digits, and treating them as such is what made
	// "set the config: token: 4096" force local-only.
	if _, err := strconv.Atoi(v); err == nil {
		return false
	}
	// A placeholder is the opposite of a secret — it exists to be substituted.
	switch strings.ToLower(v) {
	case "none", "null", "nil", "empty", "false", "true", "changeme", "redacted", "<redacted>", "xxxxxx", "******":
		return false
	}
	return true
}

// versionContext are words that precede a dotted quad which is a version, not
// an address. RE2 cannot express "not preceded by", so this is checked in code.
var versionContext = map[string]bool{
	"version": true, "v": true, "ver": true, "release": true, "rev": true,
	"build": true, "tag": true, "upgrade": true, "downgrade": true, "bump": true,
}

// validPublicIP keeps only dotted quads that are plausibly a real, routable
// address someone could be identified by.
//
// It rejects three classes that were all forcing local-only routing needlessly:
// octets above 255 and version-like context ("version 1.2.3.4"), addresses that
// are part of a longer dotted run ("1.2.3.4.5"), and non-public ranges —
// loopback, private, link-local, multicast and reserved. None of those identify
// a person, and 127.0.0.1 in particular appears constantly in dev prompts.
func validPublicIP(prompt string, loc []int) bool {
	octets := make([]int, 4)
	for i := 0; i < 4; i++ {
		s, e := loc[2+i*2], loc[3+i*2]
		if s < 0 {
			return false
		}
		n, err := strconv.Atoi(prompt[s:e])
		if err != nil || n > 255 {
			return false
		}
		// A leading zero means it is not a normal address spelling.
		if len(prompt[s:e]) > 1 && prompt[s] == '0' {
			return false
		}
		octets[i] = n
	}

	// Part of a longer dotted run, e.g. the "1.2.3.4" inside "1.2.3.4.5".
	if loc[0] > 0 {
		if c := prompt[loc[0]-1]; c == '.' || (c >= '0' && c <= '9') {
			return false
		}
	}
	if loc[1] < len(prompt) && prompt[loc[1]] == '.' {
		if loc[1]+1 < len(prompt) && prompt[loc[1]+1] >= '0' && prompt[loc[1]+1] <= '9' {
			return false
		}
	}

	if precededByVersionWord(prompt, loc[0]) {
		return false
	}

	switch {
	case octets[0] == 0, // unspecified / "this network"
		octets[0] == 10,                                        // private
		octets[0] == 127,                                       // loopback
		octets[0] >= 224,                                       // multicast + reserved + broadcast
		octets[0] == 169 && octets[1] == 254,                   // link-local
		octets[0] == 192 && octets[1] == 168,                   // private
		octets[0] == 172 && octets[1] >= 16 && octets[1] <= 31: // private
		return false
	}
	return true
}

// precededByVersionWord reports whether the word immediately before start is one
// that makes a dotted quad a version rather than an address.
func precededByVersionWord(prompt string, start int) bool {
	i := start
	for i > 0 && (prompt[i-1] == ' ' || prompt[i-1] == '\t') {
		i--
	}
	end := i
	for i > 0 {
		c := prompt[i-1]
		if c == ' ' || c == '\t' || c == '\n' {
			break
		}
		i--
	}
	return versionContext[strings.ToLower(strings.Trim(prompt[i:end], ".,:;()[]"))]
}
