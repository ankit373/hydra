// SPDX-License-Identifier: MIT

package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

func fakeHead(t *testing.T, id, content string) provider.Head {
	t.Helper()
	path := filepath.Join(t.TempDir(), id)
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	return provider.Head{ID: id, Executable: path}
}

// First sight of a binary is a baseline, not a finding, flagging every head
// as "changed" on first run would train the reader to ignore the signal.
func TestFingerprintHeads_FirstRunBaselinesRatherThanAlarms(t *testing.T) {
	testutil.NewSandbox(t)
	sc := FingerprintHeads([]provider.Head{fakeHead(t, "claude", "v1")})
	if sc.New != 1 || sc.Changed != 0 {
		t.Fatalf("SupplyChain = %+v, want 1 new and 0 changed", sc)
	}
	if !strings.Contains(supplyChainCheck(sc).Status, "baselined") {
		t.Errorf("Status = %q, want it to read as a baseline", supplyChainCheck(sc).Status)
	}
}

// Replacing the binary is the rug-pull pattern and must be caught.
func TestFingerprintHeads_DetectsAReplacedBinary(t *testing.T) {
	testutil.NewSandbox(t)
	h := fakeHead(t, "claude", "v1")

	if sc := FingerprintHeads([]provider.Head{h}); sc.New != 1 {
		t.Fatalf("baseline run = %+v", sc)
	}
	// Same path, different content, and a different size so the cheap
	// fingerprint moves, which is what triggers the re-hash.
	if err := os.WriteFile(h.Executable, []byte("v2-different-length"), 0o700); err != nil {
		t.Fatal(err)
	}

	sc := FingerprintHeads([]provider.Head{h})
	if sc.Changed != 1 {
		t.Fatalf("SupplyChain = %+v, want the replacement detected", sc)
	}
	if sc.Binaries[0].Previous == "" || sc.Binaries[0].Previous == sc.Binaries[0].SHA256 {
		t.Error("the prior hash was not retained, so the change cannot be evidenced")
	}
	c := supplyChainCheck(sc)
	if !strings.Contains(c.Status, "CHANGED") || !strings.Contains(c.Detail, "claude") {
		t.Errorf("check = %+v, want it to name the changed head", c)
	}
}

// An unchanged binary must stay quiet on every subsequent run.
func TestFingerprintHeads_UnchangedBinaryIsNotAFinding(t *testing.T) {
	testutil.NewSandbox(t)
	h := fakeHead(t, "codex", "stable")
	FingerprintHeads([]provider.Head{h})

	sc := FingerprintHeads([]provider.Head{h})
	if sc.New != 0 || sc.Changed != 0 {
		t.Errorf("SupplyChain = %+v, want silence on an unchanged binary", sc)
	}
}

// Heads with no local artifact (API keys, ports) have nothing to fingerprint
// and must not be counted as unreadable.
func TestFingerprintHeads_SkipsHeadsWithNoExecutable(t *testing.T) {
	testutil.NewSandbox(t)
	sc := FingerprintHeads([]provider.Head{{ID: "openai"}, {ID: "ollama/qwen"}})
	if len(sc.Binaries) != 0 || sc.Unfingerprintable != 0 {
		t.Errorf("SupplyChain = %+v, want executables-only and no error count", sc)
	}
}

// LLM03 was a hardcoded permanent Gap; it is now earned by actually tracking
// binaries, and must fall back to Gap when nothing is tracked.
func TestLLM03SupplyChain_EarnedNotHardcoded(t *testing.T) {
	if got := llm03SupplyChain(SupplyChain{}).Status; got != Gap {
		t.Errorf("no binaries tracked: Status = %q, want Gap", got)
	}
	tracked := llm03SupplyChain(SupplyChain{Binaries: []HeadBinary{{HeadID: "claude"}}})
	if tracked.Status != Configured {
		t.Errorf("binaries tracked: Status = %q, want Configured", tracked.Status)
	}
	// It must not overclaim: this is change detection, not provenance.
	if !strings.Contains(tracked.Detail, "origin is not verified") {
		t.Errorf("Detail = %q, want the limitation stated", tracked.Detail)
	}
}
