// SPDX-License-Identifier: MIT

package editor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readBack returns what is on disk at file, failing the test if it cannot.
func readBack(t *testing.T, file string) string {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// Most models fence a file and drop the markers. That reply was turned into ""
// before the fence stripper saw it, so a right answer was discarded (#1164).
func TestEdit_AFencedAnswerWithNoMarkersIsWritten(t *testing.T) {
	repo := editSandbox(t, "```go\npackage main\n\nfunc main() {}\n```")
	file := filepath.Join(repo, "main.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	req := Request{File: file, Enum: "MODERATE", Prompt: "add main"}
	res, err := Edit(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, error %q: a fenced answer was discarded", res.Status, res.Error)
	}
	got := readBack(t, file)
	if got != "package main\n\nfunc main() {}\n" {
		t.Errorf("file = %q, want the fenced content without the fence", got)
	}
}

// An empty fence is still empty. The failure names the head that produced it,
// so the rejected answer is an observation (#1164, #1171).
func TestEdit_AnEmptyFencedAnswerIsAnEmptyReplacement(t *testing.T) {
	repo := editSandbox(t, "```\n```")
	file := filepath.Join(repo, "keep.go")
	original := "package main\n"
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	req := Request{File: file, Enum: "MODERATE", Prompt: "x"}
	res, err := Edit(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "fail" || res.Error != "empty_replacement" {
		t.Fatalf("status %q error %q, want fail with empty_replacement", res.Status, res.Error)
	}
	if res.Head == "" {
		t.Error("the failure names no head, so the rejected answer trains nothing")
	}
	got := readBack(t, file)
	if got != original {
		t.Errorf("the file was modified: %q", got)
	}
}

// Inner fences or prose make the outer pair ambiguous, so the reply is refused
// rather than cut at a guess.
func TestSoleFencedBlock(t *testing.T) {
	tests := []struct{ name, in, want string }{
		// Taken: exactly one block, whitespace around it ignored.
		{"one block", "```go\nx := 1\n```", "```go\nx := 1\n```"},
		{"surrounding whitespace", "\n```\nx\n```\n\n", "```\nx\n```"},
		{"crlf", "```\r\nx\r\n```", "```\nx\n```"},

		// Refused: anything else.
		{"prose before", "Here it is:\n```\nx\n```", ""},
		{"inner fence", "```md\n```sh\nls\n```\n```", ""},
		{"unclosed", "```go\nx := 1", ""},
		{"no fence", "x := 1", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := soleFencedBlock(tt.in); got != tt.want {
				t.Errorf("soleFencedBlock(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// A file holding the literal markers, echoed back unchanged, used to be cut at
// the end marker and reported ok (#1165).
func TestEdit_AFileContainingTheMarkersRoundTrips(t *testing.T) {
	original := strings.Join([]string{
		"package parallel",
		"",
		"const (",
		"	markerStart = \"<<<HYDRA_FILE_START>>>\"",
		"	markerEnd   = \"<<<HYDRA_FILE_END>>>\"",
		")",
		"",
		"func after() {}",
		"",
	}, "\n")

	echoed := strings.TrimSuffix(original, "\n")
	repo := editSandbox(t, marked(original, echoed))
	file := filepath.Join(repo, "parallel.go")
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	req := Request{File: file, Enum: "MODERATE", Prompt: "no change"}
	res, err := Edit(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, error %q", res.Status, res.Error)
	}
	got := readBack(t, file)
	if got != original {
		t.Errorf("the file did not round-trip: %d bytes in, %d bytes out", len(original), len(got))
	}
}
