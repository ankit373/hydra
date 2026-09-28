// SPDX-License-Identifier: MIT

package security

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
)

// An Executable is the evidence, not a name list: it means Hydra hands the
// work to another program instead of composing the request, which is exactly
// where the egress gate stops being able to see.
func TestAssessBoundary_ClassifiesOnWhoComposesTheRequest(t *testing.T) {
	b := AssessBoundary([]provider.Head{
		{ID: "claude", Executable: "/usr/local/bin/claude"},
		{ID: "ollama/qwen2.5", Endpoint: "http://127.0.0.1:11434", LocalOnly: true},
		{ID: "openrouter", Endpoint: "https://openrouter.ai"},
		{ID: "ollama", Executable: "/usr/local/bin/ollama", LocalOnly: true},
	})

	if got, want := strings.Join(b.Opaque, ","), "claude,ollama"; got != want {
		t.Errorf("Opaque = %q, want %q: a head with an executable is a separate program", got, want)
	}
	if got, want := strings.Join(b.Governed, ","), "ollama/qwen2.5,openrouter"; got != want {
		t.Errorf("Governed = %q, want %q: an HTTP head is one Hydra composes the request for", got, want)
	}
	// A local model served over HTTP is governed, not opaque: Hydra builds
	// that request itself, so the gate sees it whether or not it leaves.
	for _, id := range b.Opaque {
		if id == "ollama/qwen2.5" {
			t.Error("a local HTTP head was classed as opaque, but the gate does see its payload")
		}
	}
}

// The local-only subset narrows the hole and must not be reported as fully
// closing it: internal/gateway enforces it, but a head that ignores its
// proxy environment and opens a raw socket is still outside what Hydra can
// see, the same residual gap every other opaque head has.
func TestBoundary_LocalOnlySubprocessesStillBreakTheGuarantee(t *testing.T) {
	b := AssessBoundary([]provider.Head{
		{ID: "ollama", Executable: "/usr/local/bin/ollama", LocalOnly: true},
	})

	if len(b.OpaqueLocalOnly) != 1 {
		t.Fatalf("OpaqueLocalOnly = %v, want the local-only subprocess named", b.OpaqueLocalOnly)
	}
	if len(b.Opaque) != 1 {
		t.Error("a local-only subprocess left the Opaque list, so the report would imply Hydra can see inside it")
	}
	if b.Total() {
		t.Error("Total() is true with a subprocess head present: a residual raw-socket gap remains, so this cannot be reported as closed")
	}

	detail := boundaryCheck(b).Detail
	if !strings.Contains(detail, "network gate") {
		t.Errorf("the check does not mention the enforcement gate: %q", detail)
	}
	if !strings.Contains(detail, "narrows the hole rather than closing it") {
		t.Errorf("the check does not say the gap is narrowed rather than closed: %q", detail)
	}
}

// Total holds only where nothing opaque can be routed to, and the check has to
// say so plainly rather than leaving it to be read off a percentage.
func TestBoundaryCheck_StatesEachCase(t *testing.T) {
	governed := AssessBoundary([]provider.Head{
		{ID: "ollama/qwen2.5", Endpoint: "http://127.0.0.1:11434", LocalOnly: true},
	})
	if !governed.Total() {
		t.Error("Total() is false with only HTTP heads, where the gate does see every byte")
	}
	if c := boundaryCheck(governed); !strings.HasPrefix(c.Status, "total") {
		t.Errorf("Status = %q, want it to lead with the guarantee being total", c.Status)
	}

	mixed := boundaryCheck(AssessBoundary([]provider.Head{
		{ID: "claude", Executable: "/bin/claude"},
		{ID: "openrouter", Endpoint: "https://openrouter.ai"},
	}))
	if !strings.Contains(mixed.Status, "1 of 2") {
		t.Errorf("Status = %q, want the proportion", mixed.Status)
	}
	// Naming the head is the actionable half; a count alone tells nobody what
	// to do about it.
	if !strings.Contains(mixed.Detail, "claude") {
		t.Errorf("Detail = %q, want it to name the opaque head", mixed.Detail)
	}

	none := boundaryCheck(AssessBoundary(nil))
	if !strings.Contains(none.Status, "no heads") {
		t.Errorf("Status = %q on an empty estate, want it to say nothing was classified", none.Status)
	}
}

// The CLI prints the same list, so the bound lives in one place. Without it a
// machine with fifteen agy models turns one sentence into a paragraph.
func TestHeadList_BoundsAWideEstate(t *testing.T) {
	if got, want := HeadList([]string{"a", "b"}), "a, b"; got != want {
		t.Errorf("HeadList = %q, want %q", got, want)
	}
	got := HeadList([]string{"a", "b", "c", "d", "e", "f"})
	if want := "a, b, c, d and 2 more"; got != want {
		t.Errorf("HeadList = %q, want %q", got, want)
	}
	if HeadList(nil) != "" {
		t.Errorf("HeadList(nil) = %q, want empty", HeadList(nil))
	}
}
