// SPDX-License-Identifier: MIT

package policy

import "strings"

// injectionMarkers are classic phrases used to try to override a model's
// instructions with content it was only supposed to treat as data. This is a
// heuristic keyword scan — not a classifier, not exhaustive, trivially evaded
// by anyone who tries. It exists to leave an audit trail, not to prevent an
// attack: no pattern-based defense closes this hole architecturally.
var injectionMarkers = []string{
	"ignore previous instructions",
	"ignore the previous instructions",
	"ignore all previous instructions",
	"disregard the above",
	"disregard previous instructions",
	"disregard all prior instructions",
	"forget your previous instructions",
	"forget all previous instructions",
	"new instructions:",
	"system prompt:",
	"do anything now",
}

// ContainsInjectionMarkers reports whether the prompt contains a classic
// prompt-injection phrase.
func ContainsInjectionMarkers(req Request) bool {
	_, ok := InjectionMarker(req)
	return ok
}

// InjectionMarker returns the specific phrase that matched, if any.
func InjectionMarker(req Request) (string, bool) {
	lower := strings.ToLower(req.Prompt)
	for _, phrase := range injectionMarkers {
		if strings.Contains(lower, phrase) {
			return phrase, true
		}
	}
	return "", false
}
