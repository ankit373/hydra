// SPDX-License-Identifier: MIT

package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/testutil"
)

// A human's approve or reject is the strongest ground truth Hydra gets, and it
// reaches calibration only through the head that wrote the file. Reading one
// log for that credited nothing to every file `hyctl parallel` touched (#1032).

func writeLogAt(t *testing.T, name, body string, at time.Time) string {
	t.Helper()
	dir := filepath.Join(config.Dir(), "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestHeadIDForFile_FindsAFileABatchEdited(t *testing.T) {
	testutil.NewSandbox(t)
	batched := filepath.FromSlash("/repo/a.go")
	other := filepath.FromSlash("/repo/b.go")

	writeLogAt(t, "last_parallel.json", `[
	  {"mode":"edit","file":`+quote(batched)+`,"head":"ollama/qwen"},
	  {"mode":"edit","file":`+quote(other)+`,"head":"ollama/coder"}
	]`, time.Now())

	if got := headIDForFile(batched); got != "ollama/qwen" {
		t.Errorf("head for a batch-edited file = %q, want ollama/qwen", got)
	}
	// Per file, not per run: a fan-out is the case where they differ.
	if got := headIDForFile(other); got != "ollama/coder" {
		t.Errorf("head for the second file = %q, want ollama/coder", got)
	}
	if got := headIDForFile(filepath.FromSlash("/repo/never.go")); got != "" {
		t.Errorf("a file in no log resolved to %q, want no head", got)
	}
}

func TestHeadIDForFile_PrefersWhicheverRunWroteTheFileLast(t *testing.T) {
	testutil.NewSandbox(t)
	file := filepath.FromSlash("/repo/a.go")
	old := time.Now().Add(-time.Hour)

	// The same file in both logs. Crediting the older run would attribute the
	// work to a head that did not do it, which is worse than finding none.
	writeLogAt(t, "last_parallel.json", `[{"mode":"edit","file":`+quote(file)+`,"head":"batch-head"}]`, old)
	writeLogAt(t, "last_edit.json", `{"file":`+quote(file)+`,"head_id":"single-head"}`, time.Now())
	if got := headIDForFile(file); got != "single-head" {
		t.Errorf("head = %q, want the newer log's single-head", got)
	}

	writeLogAt(t, "last_edit.json", `{"file":`+quote(file)+`,"head_id":"single-head"}`, old)
	writeLogAt(t, "last_parallel.json", `[{"mode":"edit","file":`+quote(file)+`,"head":"batch-head"}]`, time.Now())
	if got := headIDForFile(file); got != "batch-head" {
		t.Errorf("head = %q, want the newer log's batch-head", got)
	}
}

func TestHeadIDForFile_FallsBackWhenTheNewerLogDoesNotNameTheFile(t *testing.T) {
	testutil.NewSandbox(t)
	batched := filepath.FromSlash("/repo/a.go")

	// A later single edit of a different file must not hide the batch's answer.
	writeLogAt(t, "last_parallel.json", `[{"mode":"edit","file":`+quote(batched)+`,"head":"batch-head"}]`, time.Now().Add(-time.Hour))
	writeLogAt(t, "last_edit.json", `{"file":"/repo/elsewhere.go","head_id":"single-head"}`, time.Now())

	if got := headIDForFile(batched); got != "batch-head" {
		t.Errorf("head = %q, want batch-head from the older log that names the file", got)
	}
}

// quote renders a path as a JSON string, so a Windows separator cannot break
// the fixture it is embedded in.
func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
