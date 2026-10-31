// SPDX-License-Identifier: MIT

package policy

// Classification is a prompt's PII/injection-marker verdict. DetectPII and
// InjectionMarker are pure functions of the prompt text, so identical content
// classifies identically every time a fallback loop or swarm fan-out checks
// another candidate against it, Classify runs both once; callers should
// reuse the result instead of re-running the detectors per candidate.
type Classification struct {
	PII        bool
	PIITypes   []string
	FlagReason string
}

// Classify runs DetectPII and InjectionMarker on prompt once.
func Classify(prompt string) Classification {
	req := Request{Prompt: prompt}
	var c Classification
	if c.PIITypes = DetectPII(req); len(c.PIITypes) > 0 {
		c.PII = true
	}
	if reason, ok := InjectionMarker(req); ok {
		c.FlagReason = reason
	}
	return c
}
