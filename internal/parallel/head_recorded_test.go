// SPDX-License-Identifier: MIT

package parallel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ankit373/hydra/internal/config"
)

// A fan-out is the case where "which head wrote this file" differs per file,
// and it was the one result shape that did not carry it. `hyctl review` could
// then only find a head in last_edit.json, which a batch never writes, so a
// human's approve or reject trained nothing (#1032).
func TestEdit_TheResultAndTheReviewLogBothNameTheHead(t *testing.T) {
	repo := editSandbox(t, marked("package main\n\nfunc main() {}"))
	file := filepath.Join(repo, "main.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := runEdit(t, Task{
		Label: "edit main", Enum: "MODERATE", File: file,
		Prompt: "add an empty main", Validate: boolPtr(false),
	})
	if got.Status != "ok" {
		t.Fatalf("status = %q, error %q", got.Status, got.Error)
	}
	if got.Head != "cody" {
		t.Errorf("result head = %q, want the head the sandbox routes to", got.Head)
	}

	// last_parallel.json is what hyctl review reads, so the field has to
	// survive persistence, not just the in-memory result.
	raw, err := os.ReadFile(filepath.Join(config.Dir(), "logs", "last_parallel.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		File string `json:"file"`
		Head string `json:"head"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Head != "cody" {
		t.Errorf("last_parallel.json = %s, want one row naming the head", raw)
	}
}
