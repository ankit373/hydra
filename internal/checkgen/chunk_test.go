// SPDX-License-Identifier: MIT

package checkgen

import (
	"fmt"
	"strings"
	"testing"
)

// wide is n top-level number fields, f1..fn.
func wide(n int) []Field {
	out := make([]Field, n)
	for i := range out {
		out[i] = Field{Name: fmt.Sprintf("f%d", i+1), Type: "number"}
	}
	return out
}

func TestChunkFields_SplitsAt30(t *testing.T) {
	var sizes []int
	for _, c := range ChunkFields(wide(90), 30) {
		sizes = append(sizes, len(c))
	}
	if fmt.Sprint(sizes) != "[30 30 30]" {
		t.Fatalf("90 fields split as %v, want three of 30", sizes)
	}
	// An object's fields are not split across chunks: a.x and a.y move to the next chunk together.
	fields := append(wide(29), Field{Name: "a.x", Required: true}, Field{Name: "a.y"}, Field{Name: "z"})
	chunks := ChunkFields(fields, 30)
	if len(chunks) != 2 || len(chunks[0]) != 29 || chunks[1][0].Name != "a.x" || chunks[1][1].Name != "a.y" {
		t.Fatalf("chunks = %v, want a.x and a.y together in the second", chunks)
	}
	if got := ChunkFields(wide(3), 30); len(got) != 1 {
		t.Errorf("3 fields in %d chunks, want 1", len(got))
	}
}

func TestMerge_ReportsConflict(t *testing.T) {
	merged, conflicts := MergeObjects([]map[string]any{{"a": 1, "b": 2}, {"c": 3}, {"b": 9}})
	if len(merged) != 3 || merged["b"] != 2 {
		t.Fatalf("merged = %v, want b kept from the first chunk", merged)
	}
	if len(conflicts) != 1 || !strings.Contains(conflicts[0], `"b" came from chunks 1 and 3`) {
		t.Fatalf("conflicts = %q", conflicts)
	}
}

func TestChunkPrompt_KeepsTheRequestAsPrefix(t *testing.T) {
	p := ChunkPrompt("Pull the form.", wide(2))
	if !strings.HasPrefix(p, "Pull the form.\n\n") || !strings.HasSuffix(p, ": f1, f2") {
		t.Errorf("prompt = %q", p)
	}
}
