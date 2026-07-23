package parallel

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRun_EmptyBatch(t *testing.T) {
	_, err := Run(context.Background(), nil)
	if err == nil {
		t.Fatal("Run(nil) = nil error, want error")
	}
}

// Run must reject a batch where two edit tasks target the same file, before
// dispatching anything.
func TestRun_DuplicateFileConflict(t *testing.T) {
	tasks := []Task{
		{Label: "a", Enum: "SIMPLE", File: "/abs/path/foo.go", Prompt: "x"},
		{Label: "b", Enum: "SIMPLE", File: "/abs/path/foo.go", Prompt: "y"},
	}
	_, err := Run(context.Background(), tasks)
	if err == nil {
		t.Fatal("Run(duplicate file) = nil error, want conflict error")
	}
	if !strings.Contains(err.Error(), "conflict") {
		t.Errorf("error = %q, want it to mention conflict", err.Error())
	}
}

func TestExtractContent(t *testing.T) {
	body := markerStart + "\nhello\nworld\n" + markerEnd
	if got := extractContent("prose\n" + body + "\ntrailing"); got != "hello\nworld" {
		t.Errorf("extractContent(both) = %q, want %q", got, "hello\nworld")
	}
	if got := extractContent("no markers here"); got != "" {
		t.Errorf("extractContent(none) = %q, want empty", got)
	}
}

func TestStripOuterFence(t *testing.T) {
	if got := stripOuterFence("```ts\nconst x = 1\n```"); got != "const x = 1" {
		t.Errorf("stripOuterFence() = %q, want %q", got, "const x = 1")
	}
}

func TestFileExt(t *testing.T) {
	if got := fileExt("main.go"); got != "go" {
		t.Errorf("fileExt = %q, want go", got)
	}
	if got := fileExt("Makefile"); got != "" {
		t.Errorf("fileExt(Makefile) = %q, want empty", got)
	}
}

// Result marshals to its raw JSON verbatim.
func TestResult_MarshalJSON(t *testing.T) {
	r := Result{raw: json.RawMessage(`{"label":"a","status":"ok"}`)}
	out, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"label":"a","status":"ok"}` {
		t.Errorf("Marshal = %s, want raw passthrough", out)
	}
	if string(r.Raw()) != `{"label":"a","status":"ok"}` {
		t.Errorf("Raw() mismatch")
	}
}
