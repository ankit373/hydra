// SPDX-License-Identifier: MIT

package policy

import (
	"fmt"
	"sort"
	"strings"
)

// Redact replaces every detected secret or identifier with a labelled
// placeholder, returning the redacted text and the detector names that fired.
//
// It walks the same `detectors` list and the same validators as DetectPII, so
// redaction cannot disagree with detection: anything that would mark content as
// PII is exactly what gets replaced. A second detection pass over the result is
// the test that keeps that honest.
//
// The label is kept because the *kind* of secret is the part worth retaining,
// "this response contained an AWS key" is a finding; the key itself is a
// liability.
func Redact(prompt string) (string, []string) {
	type span struct {
		start, end int
		name       string
	}
	var spans []span
	var names []string
	for _, d := range detectors {
		hit := false
		for _, loc := range d.re.FindAllStringSubmatchIndex(prompt, -1) {
			if d.valid != nil && !d.valid(prompt, loc) {
				continue
			}
			spans = append(spans, span{start: loc[0], end: loc[1], name: d.name})
			hit = true
		}
		if hit {
			names = append(names, d.name)
		}
	}
	if len(spans) == 0 {
		return prompt, nil
	}

	// Detectors overlap by design, an "authorization: Bearer eyJ..." hit is
	// also a jwt hit. Emitting both placeholders would corrupt the text, so
	// overlapping spans merge into the earliest-starting, widest one.
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		return spans[i].end > spans[j].end
	})

	var b strings.Builder
	last := 0
	for _, s := range spans {
		if s.start < last {
			continue // already inside a placeholder
		}
		b.WriteString(prompt[last:s.start])
		fmt.Fprintf(&b, "[REDACTED:%s]", s.name)
		last = s.end
	}
	b.WriteString(prompt[last:])
	return b.String(), names
}
