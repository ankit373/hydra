// SPDX-License-Identifier: MIT

package mcpregistry

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// FindingKind names what a description rule looks for.
type FindingKind string

const (
	FindingToolDirection FindingKind = "tool-direction"
	FindingFileRead      FindingKind = "file-read"
	FindingDataSend      FindingKind = "data-send"
	FindingConcealment   FindingKind = "concealment"
	FindingHiddenChars   FindingKind = "hidden-characters"
	FindingOverLong      FindingKind = "over-long"
)

// DescriptionFinding is one span of a tool description that reads as written for the
// model, not the user. Start and End are byte offsets. A heuristic, so it never quarantines.
type DescriptionFinding struct {
	Tool  string      `json:"tool"`
	Kind  FindingKind `json:"kind"`
	Start int         `json:"start"`
	End   int         `json:"end"`
	Text  string      `json:"text"`
}

func (f DescriptionFinding) String() string {
	return fmt.Sprintf("%s: %s at bytes %d-%d: %q", f.Tool, f.Kind, f.Start, f.End, f.Text)
}

// maxDescriptionBytes is a starting default, not a measured cut. A real description padded
// with a long usage example can pass it, which is one reason over-long is only a finding.
const maxDescriptionBytes = 2048

// descriptionRule finds the [start, end) byte spans of one kind of finding.
type descriptionRule struct {
	kind  FindingKind
	spans func(desc string) [][]int
}

// descriptionRules is every check CheckDescriptions runs, in report order.
var descriptionRules = []descriptionRule{
	{kind: FindingToolDirection, spans: matchAny(
		`\b(?:before|after|instead of|prior to)\s+(?:using|calling|invoking|answering|responding|replying)\b`,
		`\b(?:always|also|first)\s+(?:call|invoke)\b`,
		`\b(?:call|invoke|use)\s+(?:the\s+)?\x60?[a-z][a-z0-9]*_[a-z0-9_]+\x60?\s+(?:first|too|as well)\b`,
		`\bdo not (?:use|call|invoke) (?:any )?other tools?\b`,
	)},
	{kind: FindingFileRead, spans: matchAny(
		`\B\.(?:ssh|aws|gnupg|kube|docker|netrc|npmrc|pypirc|git-credentials|env)\b`,
		`\B\.(?:bash_history|zsh_history)\b`,
		`\bid_(?:rsa|ed25519|ecdsa|dsa)\b`,
		`/etc/(?:passwd|shadow)\b`,
		`\b(?:mcp|mcp_config|claude_desktop_config)\.json\b`,
		`\b(?:read|open|cat|load)\s+(?:the\s+)?(?:contents?\s+of\s+)?(?:~|\$HOME|%USERPROFILE%)[/\\]\S*`,
	)},
	{kind: FindingDataSend, spans: matchAny(
		`\b(?:send|post|upload|forward|transmit|report|exfiltrate)\b[^.\n]{0,60}?\bto\s+https?://\S+`,
		`\b(?:send|post|upload|forward|transmit|report|email)\b[^.\n]{0,60}?\bto\s+[\w.+-]+@[\w-]+\.[\w.]+`,
		`\b(?:pass|include|put)\s+(?:its|their|that|this)\s+(?:contents?|output|text|data)\s+(?:as|in|into)\b`,
	)},
	{kind: FindingConcealment, spans: matchAny(
		`\b(?:do not|don't|never)\s+(?:tell|inform|mention|reveal|show|notify|alert)\b[^.\n]{0,40}?\buser\b`,
		`\bwithout\s+(?:telling|informing|notifying|asking|alerting)\s+the\s+user\b`,
		`\bignore\s+(?:all\s+|any\s+)?(?:previous|prior|above|earlier)\s+instructions\b`,
		`</?(?:important|system|instructions?)>`,
		`<!--[\s\S]*?-->`,
	)},
	{kind: FindingHiddenChars, spans: matchAny(
		`[\x{00AD}\x{180E}\x{200B}-\x{200F}\x{202A}-\x{202E}\x{2060}-\x{2064}\x{2066}-\x{2069}\x{FEFF}]+`,
		`[\x{E0000}-\x{E007F}\x00-\x08\x0B\x0C\x0E-\x1F\x7F]+`,
	)},
	{kind: FindingOverLong, spans: overLong},
}

// matchAny joins the alternatives into one case-insensitive pattern, so the
// spans one rule reports never overlap each other.
func matchAny(alternatives ...string) func(string) [][]int {
	re := regexp.MustCompile(`(?i)(?:` + strings.Join(alternatives, `|`) + `)`)
	return func(desc string) [][]int { return re.FindAllStringIndex(desc, -1) }
}

// overLong reports the tail past maxDescriptionBytes, cut on a rune boundary.
func overLong(desc string) [][]int {
	if len(desc) <= maxDescriptionBytes {
		return nil
	}
	start := maxDescriptionBytes
	for start > 0 && !utf8.RuneStart(desc[start]) {
		start--
	}
	return [][]int{{start, len(desc)}}
}

// CheckDescriptions runs every rule over every tool description, sorted by tool
// and then position so the same list always reports the same way.
func CheckDescriptions(tools map[string]Tool) []DescriptionFinding {
	var findings []DescriptionFinding
	for _, t := range tools {
		for _, rule := range descriptionRules {
			for _, span := range rule.spans(t.Description) {
				start, end := span[0], span[1]
				finding := DescriptionFinding{
					Tool:  t.Name,
					Kind:  rule.kind,
					Start: start,
					End:   end,
					Text:  t.Description[start:end],
				}
				findings = append(findings, finding)
			}
		}
	}
	slices.SortStableFunc(findings, func(a, b DescriptionFinding) int {
		if c := strings.Compare(a.Tool, b.Tool); c != 0 {
			return c
		}
		return a.Start - b.Start
	})
	return findings
}
