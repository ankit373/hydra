package editor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractContent(t *testing.T) {
	body := markerStart + "\nhello\nworld\n" + markerEnd
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"both markers", "prose\n" + body + "\ntrailing", "hello\nworld"},
		{"end only", "hello\nworld\n" + markerEnd + "\njunk", "hello\nworld"},
		{"start only", "prose\n" + markerStart + "\nhello\nworld", "hello\nworld"},
		{"neither", "just some text", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractContent(tt.in); got != tt.want {
				t.Errorf("extractContent() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStripOuterFence(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"fenced with lang", "```go\nx := 1\n```", "x := 1"},
		{"fenced bare", "```\nx := 1\n```", "x := 1"},
		{"no fence", "x := 1", "x := 1"},
		{"open fence only", "```go\nx := 1", "x := 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripOuterFence(tt.in); got != tt.want {
				t.Errorf("stripOuterFence() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFileExt(t *testing.T) {
	cases := map[string]string{
		"foo.ts":       "ts",
		"a.b.go":       "go",
		"noext":        "",
		"/abs/path.py": "py",
	}
	for in, want := range cases {
		if got := fileExt(in); got != want {
			t.Errorf("fileExt(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFirstLine(t *testing.T) {
	if got := firstLine("  line1\nline2  "); got != "line1" {
		t.Errorf("firstLine() = %q, want %q", got, "line1")
	}
	if got := firstLine(""); got != "" {
		t.Errorf("firstLine(empty) = %q, want empty", got)
	}
}

// runValidatorCmd must substitute {file} without fragmenting paths and must
// surface the validator's exit code.
func TestRunValidatorCmd(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "present.txt")
	if err := os.WriteFile(present, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	absent := filepath.Join(dir, "absent.txt")

	if _, rc := runValidatorCmd("test -f {file}", present); rc != 0 {
		t.Errorf("validator on existing file: rc = %d, want 0", rc)
	}
	if _, rc := runValidatorCmd("test -f {file}", absent); rc == 0 {
		t.Errorf("validator on missing file: rc = 0, want non-zero")
	}
}

// diffStats falls back to in-memory line counting when there is no git root
// and no backup file.
func TestDiffStats_InMemory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("a\nb\nc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	added, removed := diffStats(file, "a\n", "", file+".no-backup", true)
	if added != 2 || removed != 0 {
		t.Errorf("diffStats grow: added=%d removed=%d, want 2/0", added, removed)
	}

	added, removed = diffStats(file, "a\nb\nc\nd\ne\n", "", file+".no-backup", true)
	if added != 0 || removed != 2 {
		t.Errorf("diffStats shrink: added=%d removed=%d, want 0/2", added, removed)
	}
}
