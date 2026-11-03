// SPDX-License-Identifier: MIT

package editor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Most models fence a file by default and drop the markers. The fence stripper
// only ever saw the empty marker extraction, so a right answer was discarded (#1164).
func TestEdit_AFencedAnswerWithNoMarkersIsWritten(t *testing.T) {
	repo := editSandbox(t, "```go\npackage main\n\nfunc main() {}\n```")
	file := filepath.Join(repo, "main.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Edit(context.Background(), Request{File: file, Enum: "MODERATE", Prompt: "add main"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, error %q: a fenced answer was discarded", res.Status, res.Error)
	}
	raw, _ := os.ReadFile(file)
	if string(raw) != "package main\n\nfunc main() {}\n" {
		t.Errorf("file = %q, want the fenced content without the fence", raw)
	}
}

// A fence with nothing in it is still nothing, and the failure names the head
// that produced it, so a rejected answer is an observation (#1164, #1171).
func TestEdit_AnEmptyFencedAnswerIsAnEmptyReplacement(t *testing.T) {
	repo := editSandbox(t, "```\n```")
	file := filepath.Join(repo, "keep.go")
	original := "package main\n"
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Edit(context.Background(), Request{File: file, Enum: "MODERATE", Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "fail" || res.Error != "empty_replacement" {
		t.Fatalf("status %q error %q, want fail with empty_replacement", res.Status, res.Error)
	}
	if res.Head == "" {
		t.Error("the failure names no head, so the rejected answer trains nothing")
	}
	if raw, _ := os.ReadFile(file); string(raw) != original {
		t.Errorf("the file was modified: %q", raw)
	}
}

// Inner fences make the outer pair ambiguous, so a markerless answer holding
// several blocks is refused rather than cut at a guess.
func TestSoleFencedBlock(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"one block", "```go\nx := 1\n```", "```go\nx := 1\n```"},
		{"surrounding whitespace", "\n```\nx\n```\n\n", "```\nx\n```"},
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

// internal/parallel/parallel.go holds the literal end marker. Echoed back
// unchanged, it used to be cut at that line and reported ok (#1165).
func TestEdit_AFileContainingTheMarkersRoundTrips(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "parallel", "parallel.go"))
	if err != nil {
		t.Fatal(err)
	}
	original := string(src)
	if !strings.Contains(original, "<<<HYDRA_FILE_END>>>") || !strings.Contains(original, "<<<HYDRA_FILE_START>>>") {
		t.Fatal("parallel.go no longer holds the markers; point this test at another file that does")
	}

	repo := editSandbox(t, marked(original, strings.TrimSuffix(original, "\n")))
	file := filepath.Join(repo, "parallel.go")
	if err := os.WriteFile(file, src, 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Edit(context.Background(), Request{File: file, Enum: "MODERATE", Prompt: "no change"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, error %q", res.Status, res.Error)
	}
	if raw, _ := os.ReadFile(file); string(raw) != original {
		t.Errorf("the file did not round-trip: %d bytes in, %d bytes out", len(original), len(raw))
	}
}
