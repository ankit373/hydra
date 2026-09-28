// SPDX-License-Identifier: MIT

package trust

import (
	"path/filepath"
	"testing"
)

func TestDomainForFile(t *testing.T) {
	cases := []struct{ in, want string }{
		{"internal/trust/sprt.go", "go"},
		{"src/App.tsx", "tsx"},
		{"/abs/path/main.PY", "py"},  // case-folded: a map key is case-sensitive
		{"  spaced/file.rs  ", "rs"}, // trimmed before the extension is taken
		{"Makefile", DefaultDomain},  // no extension is not an empty domain
		{"", DefaultDomain},
		{"archive.tar.gz", "gz"}, // filepath.Ext semantics, stated not guessed
	}
	for _, c := range cases {
		if got := DomainForFile(c.in); got != c.want {
			t.Errorf("DomainForFile(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The bug: writers derived the domain from the file while the reader looked up
// "default", so the two never met and the router saw an empty calibration. This
// pins the round trip: what a writer files for a path is what a reader asking
// about that same path finds.
func TestDomainForFile_WriterAndReaderAgreeOnOneCell(t *testing.T) {
	c, _ := New("")
	const file = "internal/auth/token.go"

	// What editor/review record after validating an edit to that file.
	writerDomain := DomainForFile(file)
	mustUpdate(t, c, "ollama/qwen3:4b", writerDomain, true, OutcomeCorrect)
	mustUpdate(t, c, "ollama/qwen3:4b", writerDomain, false, OutcomeIncorrect)

	// What a --file confidence run resolves for the same target.
	readerDomain := DomainForFile(file)
	if readerDomain != writerDomain {
		t.Fatalf("reader resolved %q, writer wrote %q", readerDomain, writerDomain)
	}
	if d := c.D("ollama/qwen3:4b", readerDomain); d <= 0 {
		t.Errorf("D = %v in the domain the reader resolves; the evidence gate would refuse the run", d)
	}
	// And the cell the old reader looked at stays empty, which is the failure.
	if d := c.D("ollama/qwen3:4b", DefaultDomain); d > 0 {
		t.Errorf("D(%q) = %v; a file-shaped outcome must not also land in the catch-all", DefaultDomain, d)
	}
}

// Absolute and relative paths for the same file must agree, since a writer sees
// whatever the caller passed and a reader may see the other form.
func TestDomainForFile_PathShapeDoesNotSplitTheCell(t *testing.T) {
	rel := "internal/trust/sprt.go"
	abs, err := filepath.Abs(rel)
	if err != nil {
		t.Fatal(err)
	}
	if DomainForFile(rel) != DomainForFile(abs) {
		t.Errorf("relative %q and absolute %q resolved differently", DomainForFile(rel), DomainForFile(abs))
	}
}

func TestUnreadableSourceKey(t *testing.T) {
	for _, bad := range []string{"model:claude-sonnet", "MODEL:x", "head:ollama/qwen", "provider:openai"} {
		if _, unreadable := UnreadableSourceKey(bad); !unreadable {
			t.Errorf("UnreadableSourceKey(%q) = readable; the router never looks that key up", bad)
		}
	}
	// A head ID is what the ensemble actually keys on, and verifier: is coherent
	// because oracle verify both writes and reads it.
	for _, ok := range []string{"ollama/qwen3:4b", "claude", "lmstudio/phi-4", "verifier:go-test"} {
		if reason, unreadable := UnreadableSourceKey(ok); unreadable {
			t.Errorf("UnreadableSourceKey(%q) flagged as unreadable: %s", ok, reason)
		}
	}
}

// #894 normalized the write path and left the read path taking the caller's
// string, so `hyctl oracle verify` with no --domain filed under "default" and
// then looked the row up under "", finding the uninformative prior however much
// history it had written. Both directions have to reach the same cell.
func TestCalibration_AnUnspecifiedDomainReadsBackEitherWayItIsAsked(t *testing.T) {
	c, err := New(filepath.Join(t.TempDir(), "calibration.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	feed(t, c, "verifier:go test", "", 50, 0, 20, 0) // written the way oracle verify writes

	blank, dflt := c.D("verifier:go test", ""), c.D("verifier:go test", DefaultDomain)
	if blank != dflt {
		t.Errorf("D read as %q = %.4f but as %q = %.4f; one history, two cells", "", blank, DefaultDomain, dflt)
	}
	// Equal at zero would satisfy the check above while reading nothing at all,
	// which is the same bug in its other direction.
	if blank == 0 {
		t.Errorf("D = 0 on 70 observations: the reader found the prior, not the history")
	}
	if l := c.LLR("verifier:go test", "", true); l == 0 {
		t.Errorf("LLR read with no domain = 0 on 70 observations")
	}
}

// The reverse pairing: written with the spelling dispatch uses, read with the
// spelling oracle verify uses.
func TestCalibration_DefaultAndBlankAreOneCellWhicheverWrote(t *testing.T) {
	c, err := New(filepath.Join(t.TempDir(), "calibration.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	feed(t, c, "claude", DefaultDomain, 30, 0, 10, 0)
	feed(t, c, "claude", "", 20, 0, 10, 0)

	rows := c.Report()
	if len(rows) != 1 {
		t.Fatalf("got %d cells, want 1: %+v", len(rows), rows)
	}
	if rows[0].N != 70 {
		t.Errorf("observations = %v, want 70 (30+10 and 20+10 in one cell)", rows[0].N)
	}
	if rows[0].Domain != DefaultDomain {
		t.Errorf("cell domain = %q, want %q: a report must name a key a reader can ask for", rows[0].Domain, DefaultDomain)
	}
}
