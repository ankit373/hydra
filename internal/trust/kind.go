// SPDX-License-Identifier: MIT

package trust

import (
	"fmt"
	"slices"
	"strings"
)

// Kinds is the one list of kinds of work. Nouns, so a served class such as
// classification.banking77 is already a <kind>.<area> key.
var Kinds = []string{
	"writing", "summary", "extraction", "classification", "translation",
	"answer", "analysis", "agent", "code",
}

// DefaultArea is the area a kind gets when the caller names none.
const DefaultArea = "general"

// Kind builds the <kind>.<area> routing key. The dot keeps it inside TaskClass's
// grammar, so it can never collide with a bare-extension cell (#888).
func Kind(kind, area string) (string, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if !slices.Contains(Kinds, kind) {
		return "", fmt.Errorf("unknown kind of work %q: want one of %s", kind, strings.Join(Kinds, ", "))
	}
	if area = strings.TrimSpace(area); area == "" {
		area = DefaultArea
	}
	return TaskClass(kind + "." + area)
}

// ParseKind reads a --kind value, <kind> or <kind>.<area>.
func ParseKind(s string) (string, error) {
	kind, area, _ := strings.Cut(s, ".")
	return Kind(kind, area)
}
