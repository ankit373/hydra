// SPDX-License-Identifier: MIT

package security

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/embed"
)

// The claim used to be a constant reading "Hydra has no RAG pipeline or vector
// store of its own". A security surface that cannot notice one appeared is
// worse than no surface, so this asserts it is read off the store.
func TestVectorCategory_TracksTheStore(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())

	c := vectorCategory()
	if c.Status != NotApplicable {
		t.Fatalf("with no store, want NotApplicable, got %v", c.Status)
	}
	if strings.Contains(c.Detail, "no RAG pipeline") {
		t.Error("the detail still asserts the old unconditional claim")
	}

	st, err := embed.OpenStore(embed.Dir(), "nomic-embed-text")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Put("0123456789abcdef", make([]float32, 8)); err != nil {
		t.Fatal(err)
	}

	c = vectorCategory()
	if c.Status != Partial {
		t.Fatalf("with a store holding vectors, want Partial, got %v", c.Status)
	}
	for _, want := range []string{"nomic-embed-text", "redacted", "invertible"} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("the detail does not mention %q: %s", want, c.Detail)
		}
	}
}
