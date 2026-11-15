// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/config"
)

// A summary with no file used to get no cell of its own and was ranked on the
// pooled record. --kind gives it one, and the cost row names the cell it routed on.
func TestDispatch_KindSetsTheRoutingCell(t *testing.T) {
	dispatchable(t, "answered")

	if _, cobraOut, err := run(t, "dispatch", "--no-stream", "--kind", "summary", "--tier", "6",
		"summarise this call in three lines"); err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	raw, err := os.ReadFile(filepath.Join(config.Dir(), "logs", "cost.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var row struct{ Domain string }
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &row); err != nil {
		t.Fatalf("one cost row expected: %v\n%s", err, raw)
	}
	if row.Domain != "summary.general" {
		t.Errorf("routed under %q, want summary.general", row.Domain)
	}
}

// Refused before anything routes: a sandbox has no heads, so a later failure
// would read as "no heads available" instead.
func TestDispatch_UnknownKindRefusedBeforeRouting(t *testing.T) {
	cliSandbox(t)

	_, _, err := run(t, "dispatch", "--dry-run", "--kind", "summarise", "q")
	if err == nil || !strings.Contains(err.Error(), "unknown kind of work") {
		t.Fatalf("err = %v, want the unknown-kind refusal", err)
	}
}

func TestDispatch_KindAndDomainTogetherRefused(t *testing.T) {
	cliSandbox(t)

	_, _, err := run(t, "dispatch", "--dry-run", "--kind", "summary", "--domain", "go", "q")
	if err == nil || !strings.Contains(err.Error(), "--kind and --domain") {
		t.Fatalf("err = %v, want both flags refused together", err)
	}
}
