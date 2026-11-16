// SPDX-License-Identifier: MIT

package checkgen

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// ChunkFields splits fields, in order, into chunks of at most max. An object's
// fields (a.b, a.c) stay in one chunk, even when that chunk passes max.
func ChunkFields(fields []Field, max int) [][]Field {
	var out [][]Field
	var cur []Field
	for i := 0; i < len(fields); {
		j := i + 1
		for j < len(fields) && parentOf(fields[j].Name) != "" && parentOf(fields[j].Name) == parentOf(fields[i].Name) {
			j++
		}
		if len(cur) > 0 && len(cur)+j-i > max {
			out, cur = append(out, cur), nil
		}
		cur = append(cur, fields[i:j]...)
		i = j
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// parentOf is the object a dotted field sits in, or "" for a top-level field.
func parentOf(name string) string {
	parent, _, _ := strings.Cut(name, ".")
	if parent == name {
		return ""
	}
	return parent
}

// ChunkPrompt asks for one chunk's fields. The request comes first and unchanged,
// so every chunk shares it as a prefix a provider cache can reuse.
func ChunkPrompt(prompt string, chunk []Field) string {
	names := make([]string, len(chunk))
	for i, f := range chunk {
		names[i] = f.Name
	}
	return prompt + "\n\nAnswer with one JSON object holding only these fields " +
		"(a dotted name is a field inside an object): " + strings.Join(names, ", ")
}

// MergeObjects joins the chunks' objects, a nil one skipped. A key two chunks both
// return is a conflict: the first chunk's value stays and the conflict is reported.
func MergeObjects(parts []map[string]any) (map[string]any, []string) {
	merged := map[string]any{}
	from := map[string]int{}
	var conflicts []string
	for i, p := range parts {
		for _, k := range slices.Sorted(maps.Keys(p)) {
			if first, ok := from[k]; ok {
				conflicts = append(conflicts, fmt.Sprintf("%q came from chunks %d and %d; kept chunk %d's", k, first+1, i+1, first+1))
				continue
			}
			merged[k], from[k] = p[k], i
		}
	}
	return merged, conflicts
}
