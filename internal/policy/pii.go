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
// pattern at all, they need code that sees the match in context (#169).
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
	// 123456789". This does trade away detection of a bare, unformatted SSN,
	// a real loss, accepted because the formatted spelling is overwhelmingly
	// more common in text and the unformatted one is unknowable in isolation.
	{name: "ssn", re: regexp.MustCompile(`\b\d{3}[-.\s]\d{2}[-.\s]\d{4}\b`)},

	// 13-19 digits, optionally grouped, that pass Luhn. Luhn rejects ~90% of
	// random digit runs, which is what keeps long ids from reading as cards.
	{name: "credit card", re: regexp.MustCompile(`\b\d{4}[- ]?\d{4}[- ]?\d{4}[- ]?\d{1,7}\b`), valid: validLuhn},

	// A number is recognised by a marker ordinary digits do not carry: a country
	// code, a parenthesised area code, the 3-3-4 grouping, or a trunk zero. A
	// bare run is refused for the reason nine bare digits are not an SSN (#1031).
	{name: "phone number", re: regexp.MustCompile(`(?:` +
		`\+[1-9]\d{0,2}[-. ]?(?:\(0\)[-. ]?)?(?:\d[-. ]?){6,14}\d` +
		`|\(\d{2,4}\)[-. ]?\d{3,4}[-. ]?\d{3,4}` +
		`|\d{3}[-. ]\d{3}[-. ]\d{4}` +
		`|0\d{2,4}[-. ]\d{2,7}(?:[-. ]\d{2,7}){0,2}` +
		`)(?: ?(?:[xX]|[eE][xX][tT]\.?) ?\d{1,6})?`), valid: validPhone},

	// Two letters, two check digits, then the account. The mod-97 checksum is
	// what keeps ordinary alphanumeric ids out, as Luhn does for cards.
	{name: "iban", re: regexp.MustCompile(`\b[A-Za-z]{2}\d{2}[A-Za-z0-9]{11,30}\b`), valid: validIBAN},

	// ── Context-dependent ────────────────────────────────────────────────────
	{name: "credentials", re: regexp.MustCompile(`(?i)\b(?:password|passwd|secret|api[-_]?key|access[-_]?key|auth[-_]?token|token|private[-_]?key)\s*[:=]\s*("[^"]*"|'[^']*'|\S+)`), valid: validCredentialValue},
	{name: "ip address", re: regexp.MustCompile(`\b(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\b`), valid: validPublicIP},
}

// ContainsPII reports whether the prompt likely contains sensitive data.
// Honors req.PII when already computed, rather than re-running the scan.
func ContainsPII(req Request) bool {
	if req.PII != nil {
		return *req.PII
	}
	return len(DetectPII(req)) > 0
}

// DetectPII returns the names of every detector that matched, deduped and in
// declaration order.
//
// The detectors have always known *what* they matched, "aws access key id" is
// a categorically different finding from "email", but the only accessor was
// ContainsPII's bool, so the distinction was discarded at the first call site
// and everything downstream could say no more than "PII". Callers that record
// or report a detection want the names; ContainsPII is now defined in terms of
// this rather than duplicating the loop, so the two can never disagree.
// DetectorNames lists every detector, in declaration order.
//
// Exported so a caller can enumerate the signals a rule may name before any
// prompt exists. DetectPII answers what fired; this answers what could.
func DetectorNames() []string {
	out := make([]string, 0, len(detectors))
	for _, d := range detectors {
		out = append(out, d.name)
	}
	return out
}

func DetectPII(req Request) []string {
	var out []string
	for _, d := range detectors {
		for _, loc := range d.re.FindAllStringSubmatchIndex(req.Prompt, -1) {
			if d.valid == nil || d.valid(req.Prompt, loc) {
				out = append(out, d.name)
				break // one entry per detector, however many times it hit
			}
		}
	}
	return out
}

// validLuhn checks the card checksum. Without it, any 16-digit identifier,
// order numbers, trace ids, forced local-only routing.
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
	// A placeholder is the opposite of a secret, it exists to be substituted.
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
// are part of a longer dotted run ("1.2.3.4.5"), and non-public ranges,
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

// E.164 bounds a number at 15 digits and no plan is shorter than 7, so a run
// outside that is an account, an id or a card rather than a number to call.
const (
	minPhoneDigits = 7
	maxPhoneDigits = 15
)

// validPhone rejects a match that is only part of a longer number, which is
// what "5500 0055 5555 5559" and the "+" in "1664525+1013904223" both are:
// RE2 has no lookbehind, so the neighbouring digits are checked here (#1031).
func validPhone(prompt string, loc []int) bool {
	if continuesLeft(prompt, loc[0]) || continuesRight(prompt, loc[1]) {
		return false
	}
	m := prompt[loc[0]:loc[1]]
	if isoDate(m) {
		return false
	}
	n, first, varied, aboveOne := 0, byte(0), false, false
	for i := 0; i < len(m); i++ {
		if isLetter(m[i]) {
			break // an extension marker; E.164's 15 bounds the number before it
		}
		if !isDigit(m[i]) {
			continue
		}
		n++
		switch {
		case first == 0:
			first = m[i]
		case m[i] != first:
			varied = true
		}
		if m[i] > '1' {
			aboveOne = true
		}
	}
	// "111-111-1111" is a placeholder and "0011 1111" is a bit mask. No plan
	// issues a number of one repeated digit, or of binary digits alone, and a
	// source tree is full of both.
	if !varied || !aboveOne {
		return false
	}
	return n >= minPhoneDigits && n <= maxPhoneDigits
}

// isoDate reports the YYYY-MM-DD shape, which the trunk-zero form otherwise
// reads as a number whenever the year starts with a zero: Go's zero time,
// "0001-01-01", appears in a great deal of ordinary source.
func isoDate(m string) bool {
	if len(m) > 10 {
		m = m[:10]
	}
	if len(m) != 10 || (m[4] != '-' && m[4] != '.') || m[4] != m[7] {
		return false
	}
	for i, c := range []byte(m) {
		if i != 4 && i != 7 && !isDigit(c) {
			return false
		}
	}
	mo := int(m[5]-'0')*10 + int(m[6]-'0')
	d := int(m[8]-'0')*10 + int(m[9]-'0')
	return mo >= 1 && mo <= 12 && d >= 1 && d <= 31
}

// continuesLeft and continuesRight report whether the number runs on past the
// match, either immediately or across one separator. That is what tells a phone
// number from the middle of a card: RE2 has no lookbehind to say it in pattern.
//
// Only the right side treats a letter as a continuation, because that is the
// side measured to need it: a match ends in a plain digit group that a UUID's
// hex runs into, while it starts with a marker no letter abuts.
func continuesLeft(prompt string, start int) bool {
	if start == 0 {
		return false
	}
	if isDigit(prompt[start-1]) {
		return true
	}
	return isPhoneSep(prompt[start-1]) && start >= 2 && isDigit(prompt[start-2])
}

func continuesRight(prompt string, end int) bool {
	if end >= len(prompt) {
		return false
	}
	if isDigit(prompt[end]) || isLetter(prompt[end]) {
		return true
	}
	return isPhoneSep(prompt[end]) && end+1 < len(prompt) && isDigit(prompt[end+1])
}

func isLetter(c byte) bool { return c|0x20 >= 'a' && c|0x20 <= 'z' }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isPhoneSep(c byte) bool { return c == '-' || c == '.' || c == ' ' || c == '\t' }

// ibanLength is the registry's length per country. Checked because mod-97 alone
// admits 1 in 97 of the hex blobs and base64 that fill a source tree, and a
// country code is what tells an account number from a random alphanumeric run.
var ibanLength = map[string]int{
	"AD": 24, "AE": 23, "AL": 28, "AT": 20, "AZ": 28, "BA": 20, "BE": 16, "BG": 22,
	"BH": 22, "BI": 27, "BR": 29, "BY": 28, "CH": 21, "CR": 22, "CY": 28, "CZ": 24,
	"DE": 22, "DJ": 27, "DK": 18, "DO": 28, "EE": 20, "EG": 29, "ES": 24, "FI": 18,
	"FK": 18, "FO": 18, "FR": 27, "GB": 22, "GE": 22, "GI": 23, "GL": 18, "GR": 27,
	"GT": 28, "HN": 28, "HR": 21, "HU": 28, "IE": 22, "IL": 23, "IQ": 23, "IS": 26,
	"IT": 27, "JO": 30, "KM": 27, "KW": 30, "KZ": 20, "LB": 28, "LC": 32, "LI": 21,
	"LT": 20, "LU": 20, "LV": 21, "LY": 25, "MA": 28, "MC": 27, "MD": 24, "ME": 22,
	"MG": 27, "MK": 19, "MN": 20, "MR": 27, "MT": 31, "MU": 30, "MZ": 25, "NI": 28,
	"NL": 18, "NO": 15, "OM": 23, "PK": 24, "PL": 28, "PS": 29, "PT": 25, "QA": 29,
	"RO": 24, "RS": 22, "RU": 33, "SA": 24, "SC": 31, "SD": 18, "SE": 24, "SI": 19,
	"SK": 24, "SM": 27, "SN": 28, "SO": 23, "ST": 25, "SV": 28, "TD": 27, "TG": 28,
	"TL": 23, "TN": 24, "TR": 26, "UA": 29, "VA": 22, "VG": 24, "XK": 20,
}

// validIBAN checks the mod-97 checksum: move the first four characters to the
// end, read letters as 10-35, and the whole number must be congruent to 1.
func validIBAN(prompt string, loc []int) bool {
	s := prompt[loc[0]:loc[1]]
	if n, ok := ibanLength[strings.ToUpper(s[:2])]; !ok || n != len(s) {
		return false
	}
	rem := 0
	for i := 0; i < len(s); i++ {
		c := s[(i+4)%len(s)]
		switch {
		case c >= '0' && c <= '9':
			rem = (rem*10 + int(c-'0')) % 97
		case c >= 'A' && c <= 'Z':
			rem = (rem*100 + int(c-'A') + 10) % 97
		case c >= 'a' && c <= 'z':
			rem = (rem*100 + int(c-'a') + 10) % 97
		default:
			return false
		}
	}
	return rem == 1
}
