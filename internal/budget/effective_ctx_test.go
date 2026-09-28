// SPDX-License-Identifier: MIT

package budget

import (
	"testing"

	"github.com/ankit373/hydra/internal/provider"
)

func localHead(id string, meta map[string]string) provider.Head {
	return provider.Head{ID: id, Provider: "llamacpp", Source: "port", LocalOnly: true, Meta: meta}
}

// A local head is budgeted at a default precisely because its window is
// unknowable. A server that reports the window it allocated has answered that,
// so the number replaces the default instead of capping it: as a cap, a server
// started with -c 32768 would still be held to 4096.
func TestWindowsForHeads_AReportedWindowReplacesTheDefault(t *testing.T) {
	heads := []provider.Head{
		localHead("llamacpp/big", map[string]string{"model_ctx": "32768"}),
		localHead("llamacpp/small", map[string]string{"model_ctx": "1024"}),
		localHead("ollama/unknown", nil),
	}

	got := WindowsForHeads(t.TempDir(), heads)

	if got["llamacpp/big"] != 32768 {
		t.Errorf("big = %d, want the 32768 the server reported, not the local default",
			got["llamacpp/big"])
	}
	if got["llamacpp/small"] != 1024 {
		t.Errorf("small = %d, want the 1024 the server reported", got["llamacpp/small"])
	}
	// Nothing reported, nothing changes.
	if got["ollama/unknown"] != localDefaultCtx {
		t.Errorf("unknown = %d, want the local default %d", got["ollama/unknown"], localDefaultCtx)
	}
}

// A ceiling still bounds a reported window: they answer different questions,
// and the tighter of the two is the one a budget has to respect.
func TestWindowsForHeads_ACeilingStillCapsAReportedWindow(t *testing.T) {
	heads := []provider.Head{localHead("llamacpp/capped", map[string]string{
		"model_ctx":     "32768",
		"model_ctx_max": "8192",
	})}

	if got := WindowsForHeads(t.TempDir(), heads)["llamacpp/capped"]; got != 8192 {
		t.Errorf("window = %d, want the 8192 ceiling to bound the reported 32768", got)
	}
}

// An unusable value is not a window of nothing.
func TestEffectiveContext_RefusesWhatItCannotRead(t *testing.T) {
	for _, raw := range []string{"", "0", "-1", "lots"} {
		if n, ok := EffectiveContext(localHead("h", map[string]string{"model_ctx": raw})); ok {
			t.Errorf("model_ctx %q read as %d, want not reported", raw, n)
		}
	}
	if n, ok := EffectiveContext(localHead("h", nil)); ok {
		t.Errorf("a head with no meta reported %d", n)
	}
}
