// SPDX-License-Identifier: MIT

package parallel

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/testutil"

	// Providers register themselves in init(), and this package does not import
	// them — so without these blanks provider.All() is empty in the test binary
	// and every dispatch finds no heads at all. cmd/hydra imports the same set.
	_ "github.com/ankit373/hydra/internal/provider/cli"
)

// The edit path is the only thing in Hydra that writes to the user's source
// files from a model's output. Every guard on it — scope, marker parsing,
// atomic write, validation, rollback — is the difference between an edit and a
// corrupted file, and none of them was covered.

// editSandbox gives a hermetic environment with a config, a workspace rooted at
// a temp repo, and a fake `claude` on PATH that replies with whatever body is
// given. That makes a real end-to-end edit run with no network and no API key.
func editSandbox(t *testing.T, reply string) (repo string) {
	t.Helper()
	s := testutil.NewSandbox(t)

	if err := config.Save(&config.Config{Cortex: "cody"}); err != nil {
		t.Fatal(err)
	}

	// The head is discovered from $PATH by the cli provider. `cody` is used
	// rather than `claude` because its capability score puts it at UITier 6, so
	// enum MODERATE routes to it — a tier-1 head is *stronger* than any enum an
	// edit task is allowed to ask for, and selection would find nothing.
	s.FakeBinary(t, "cody", testutil.EchoScript(reply))

	repo = t.TempDir()
	// A .git directory that is not a repository: workspace resolution roots
	// here, but git commands fail, so the backup path is what protects the file.
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	// As the OS reports it: macOS gives /var symlinks for temp dirs and Windows
	// normalises casing, and workspace containment compares the resolved form.
	cwd, err := os.Getwd()
	if err != nil {
		return repo
	}
	return cwd
}

func marked(content string) string {
	return "<<<HYDRA_FILE_START>>>\n" + content + "\n<<<HYDRA_FILE_END>>>"
}

func runEdit(t *testing.T, task Task) EditResult {
	t.Helper()
	results, err := Run(context.Background(), []Task{task}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	var got EditResult
	if err := json.Unmarshal(results[0].raw, &got); err != nil {
		t.Fatalf("result is not an EditResult: %v\n%s", err, results[0].raw)
	}
	return got
}

func TestEdit_WritesTheModelsContentAndReportsTheDiff(t *testing.T) {
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
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "func main() {}") {
		t.Errorf("file = %q, want the model's content", raw)
	}
	// The markers must never reach the file.
	if strings.Contains(string(raw), "HYDRA_FILE") {
		t.Errorf("the markers leaked into the file: %q", raw)
	}
	if got.LinesAdded == 0 {
		t.Errorf("LinesAdded = 0 for a file that grew: %+v", got)
	}
	// No temp file and no stale backup left in the user's tree.
	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".hydra-tmp") || strings.HasSuffix(e.Name(), ".hydra-bak") {
			t.Errorf("%s was left behind after a successful edit", e.Name())
		}
	}
}

// A ledger rule keyed on a file glob must block runEditTask against a
// matching path — mirrors editor.Edit's own resource-scoping test, since
// parallel.runEditTask duplicates that flow.
func TestEdit_LedgerResourceRuleBlocksAMatchingFile(t *testing.T) {
	repo := editSandbox(t, marked("package main\n\nfunc main() {}"))
	writeLedgerPolicy(t, ledger.Policy{Rules: []ledger.Rule{{Resource: "**/secrets/**", Decision: ledger.Deny}}})

	blocked := filepath.Join(repo, "secrets", "key.go")
	if err := os.MkdirAll(filepath.Dir(blocked), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocked, []byte("package secrets\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := runEdit(t, Task{Label: "edit", Enum: "MODERATE", File: blocked, Prompt: "add a helper", Validate: boolPtr(false)})
	if got.Status != "fail" {
		t.Errorf("status = %q, want fail — the resource rule should have blocked this edit", got.Status)
	}

	allowed := filepath.Join(repo, "main.go")
	if err := os.WriteFile(allowed, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got = runEdit(t, Task{Label: "edit", Enum: "MODERATE", File: allowed, Prompt: "add an empty main", Validate: boolPtr(false)})
	if got.Status != "ok" {
		t.Errorf("status = %q, want ok — a non-matching path must not be blocked: %q", got.Status, got.Error)
	}
}

func writeLedgerPolicy(t *testing.T, p ledger.Policy) {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	path := ledger.DefaultPolicyPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestEdit_CreatesAFileThatDoesNotExistYet(t *testing.T) {
	repo := editSandbox(t, marked("brand new"))
	file := filepath.Join(repo, "sub", "dir", "new.txt")

	got := runEdit(t, Task{
		Label: "create", Enum: "MODERATE", File: file,
		Prompt: "create it", Validate: boolPtr(false),
	})
	if got.Status != "ok" {
		t.Fatalf("status = %q, error %q", got.Status, got.Error)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("the file was not created: %v", err)
	}
	if !strings.Contains(string(raw), "brand new") {
		t.Errorf("file = %q", raw)
	}
}

// A model that answers with nothing between the markers must not truncate the
// user's file to empty. This is the worst outcome the edit path can produce.
func TestEdit_EmptyReplacementIsRefusedAndTheFileIsUntouched(t *testing.T) {
	repo := editSandbox(t, marked(""))
	file := filepath.Join(repo, "keep.go")
	original := "package main\n\n// do not lose me\n"
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	got := runEdit(t, Task{
		Label: "empty", Enum: "MODERATE", File: file,
		Prompt: "empty it", Validate: boolPtr(false),
	})

	if got.Status != "fail" {
		t.Fatalf("status = %q, want fail — an empty replacement was accepted", got.Status)
	}
	if got.Error != "empty_replacement" {
		t.Errorf("Error = %q, want empty_replacement", got.Error)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != original {
		t.Errorf("the file was modified despite the refusal:\n%q", raw)
	}
}

// A model that echoes a marker back inside its content would write it into the
// file, and the next edit would then parse against it.
//
// The shape that reaches this guard is a repeated start marker with no
// terminator: with both markers present the extractor stops at the first end
// marker and swallows any repeated start, so the only way a marker survives
// extraction is the unterminated branch.
func TestEdit_MarkerLeakageIsRefused(t *testing.T) {
	repo := editSandbox(t, "<<<HYDRA_FILE_START>>>\nline one\n<<<HYDRA_FILE_START>>>\nline two")
	file := filepath.Join(repo, "a.go")
	original := "original\n"
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	got := runEdit(t, Task{
		Label: "leak", Enum: "MODERATE", File: file,
		Prompt: "x", Validate: boolPtr(false),
	})
	if got.Status != "fail" || got.Error != "marker_leakage" {
		t.Fatalf("result = %+v, want a marker_leakage failure", got)
	}
	if raw, _ := os.ReadFile(file); string(raw) != original {
		t.Errorf("the file was modified: %q", raw)
	}
}

// No markers at all means the model ignored the instruction. Writing its prose
// into the file would replace source with an explanation.
func TestEdit_NoMarkersIsRefused(t *testing.T) {
	repo := editSandbox(t, "Sure! Here is what I would change: ...")
	file := filepath.Join(repo, "a.go")
	original := "original\n"
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	got := runEdit(t, Task{
		Label: "prose", Enum: "MODERATE", File: file,
		Prompt: "x", Validate: boolPtr(false),
	})
	if got.Status != "fail" {
		t.Fatalf("status = %q, want fail: %+v", got.Status, got)
	}
	if raw, _ := os.ReadFile(file); string(raw) != original {
		t.Errorf("prose was written into the file: %q", raw)
	}
}

// A relative path, a CORE task, and a file outside every workspace must all be
// refused before anything is dispatched.
func TestEdit_RefusesBeforeDispatching(t *testing.T) {
	repo := editSandbox(t, marked("x"))

	tests := []struct {
		name    string
		task    Task
		wantErr string
	}{{
		name:    "relative path",
		task:    Task{Label: "rel", Enum: "MODERATE", File: "relative.go", Prompt: "x"},
		wantErr: "absolute",
	}, {
		name: "CORE is the orchestrator's own job",
		task: Task{Label: "core", Enum: "CORE",
			File: filepath.Join(repo, "a.go"), Prompt: "x"},
		wantErr: "CORE",
	}, {
		name: "outside every workspace",
		task: Task{Label: "outside", Enum: "MODERATE",
			File: filepath.Join(t.TempDir(), "elsewhere.go"), Prompt: "x"},
		wantErr: "scope_rejected",
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runEdit(t, tt.task)
			if got.Status != "fail" {
				t.Fatalf("status = %q, want fail", got.Status)
			}
			if !strings.Contains(got.Error, tt.wantErr) {
				t.Errorf("Error = %q, want it to mention %q", got.Error, tt.wantErr)
			}
		})
	}
}

// A validator that rejects the edit must roll the file back. An edit that fails
// validation and stays on disk is worse than no edit at all.
func TestEdit_FailedValidationRollsTheFileBack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the workspace validator template is a POSIX command line here")
	}
	repo := editSandbox(t, marked("this will not validate"))

	// A workspace registry whose validator for .go always fails.
	writeWorkspaceYAML(t, repo, "/usr/bin/false {file}")

	file := filepath.Join(repo, "a.go")
	original := "package main\n"
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	got := runEdit(t, Task{
		Label: "validate", Enum: "MODERATE", File: file, Prompt: "x",
	})

	if got.Status != "fail" || got.Error != "validation_failed" {
		t.Fatalf("result = %+v, want a validation_failed failure", got)
	}
	if !got.RolledBack {
		t.Error("RolledBack = false; the caller cannot tell whether the file changed")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != original {
		t.Errorf("the file was left in its failed state:\n%q", raw)
	}
	if _, err := os.Stat(file + ".hydra-bak"); err == nil {
		t.Error("the backup survived the rollback, so the next edit sees a stale baseline")
	}
}

// A validator that passes leaves the edit in place.
func TestEdit_PassingValidationKeepsTheEdit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the workspace validator template is a POSIX command line here")
	}
	repo := editSandbox(t, marked("validated content"))
	writeWorkspaceYAML(t, repo, "/usr/bin/true {file}")

	file := filepath.Join(repo, "a.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := runEdit(t, Task{Label: "ok", Enum: "MODERATE", File: file, Prompt: "x"})
	if got.Status != "ok" {
		t.Fatalf("result = %+v", got)
	}
	if !got.ValidatorPassed {
		t.Error("ValidatorPassed = false on a passing validator")
	}
	if raw, _ := os.ReadFile(file); !strings.Contains(string(raw), "validated content") {
		t.Errorf("the edit was rolled back despite passing: %q", raw)
	}
}

// Results are persisted for `hyctl review` to read. A batch that writes nothing
// leaves the user with no way to see what was changed.
func TestEdit_ResultsArePersistedForReview(t *testing.T) {
	repo := editSandbox(t, marked("content"))
	file := filepath.Join(repo, "a.txt")

	if _, err := Run(context.Background(), []Task{{
		Label: "persisted", Enum: "MODERATE", File: file,
		Prompt: "x", Validate: boolPtr(false),
	}}, Options{}); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(config.Dir(), "logs", "last_parallel.json"))
	if err != nil {
		t.Fatalf("no last_parallel.json was written: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("last_parallel.json is not a JSON array: %v\n%s", err, raw)
	}
	if len(rows) != 1 || rows[0]["file"] != file || rows[0]["mode"] != "edit" {
		t.Errorf("last_parallel.json = %v, want the one edit `hyctl review` reads", rows)
	}
}

// A text task carries the head's answer through unchanged, and reads its
// --context file into the prompt.
func TestTextTask_ReturnsTheAnswerAndReadsItsContextFile(t *testing.T) {
	repo := editSandbox(t, "the model's answer")

	ctxFile := filepath.Join(repo, "context.md")
	if err := os.WriteFile(ctxFile, []byte("CONTEXT LINE"), 0o600); err != nil {
		t.Fatal(err)
	}

	results, err := Run(context.Background(), []Task{{
		Label: "ask", Enum: "MODERATE", Prompt: "question", Context: ctxFile,
	}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var got TextResult
	if err := json.Unmarshal(results[0].raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "ok" {
		t.Fatalf("status = %q, error %q", got.Status, got.Error)
	}
	if !strings.Contains(got.Output, "the model's answer") {
		t.Errorf("Output = %q", got.Output)
	}

	// A context file that does not exist must not fail the task — the prompt
	// simply goes without it.
	results, err = Run(context.Background(), []Task{{
		Label: "ask", Enum: "MODERATE", Prompt: "question",
		Context: filepath.Join(repo, "absent.md"),
	}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(results[0].raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "ok" {
		t.Errorf("a missing context file failed the task: %+v", got)
	}
}

// A task whose dispatch fails must be reported as a failed task, not as a
// failed batch — the other tasks in the fan-out still ran.
func TestRun_OneFailingTaskDoesNotFailTheBatch(t *testing.T) {
	repo := editSandbox(t, marked("fine"))

	results, err := Run(context.Background(), []Task{
		{Label: "good", Enum: "MODERATE", File: filepath.Join(repo, "a.txt"),
			Prompt: "x", Validate: boolPtr(false)},
		{Label: "bad", Enum: "MODERATE", File: "not-absolute.txt", Prompt: "x"},
	}, Options{})
	if err != nil {
		t.Fatalf("one bad task failed the whole batch: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want one per task", len(results))
	}

	statuses := map[string]string{}
	for _, r := range results {
		var e EditResult
		if err := json.Unmarshal(r.raw, &e); err != nil {
			t.Fatal(err)
		}
		statuses[e.Label] = e.Status
	}
	if statuses["good"] != "ok" || statuses["bad"] != "fail" {
		t.Errorf("statuses = %v, want good ok and bad fail", statuses)
	}
}

// ── unit-level helpers ────────────────────────────────────────────────────────

func TestStatusOf(t *testing.T) {
	if got := statusOf(json.RawMessage(`{"status":"ok"}`)); got != "ok" {
		t.Errorf("statusOf = %q, want ok", got)
	}
	// Neither garbage nor a missing field may read as a success.
	for _, raw := range []string{`not json`, `{}`, `{"status":""}`} {
		if got := statusOf(json.RawMessage(raw)); got != "unknown" {
			t.Errorf("statusOf(%s) = %q, want unknown", raw, got)
		}
	}
}

func TestRunValidate_DoesNotFragmentPathsWithSpaces(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX command lines")
	}
	// /usr/bin/test -f <path> succeeds only if the path reached it as one
	// argument. strings.Fields would have split it at the space.
	dir := t.TempDir()
	spaced := filepath.Join(dir, "a file with spaces.go")
	if err := os.WriteFile(spaced, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rc := runValidate("/bin/test -f {file}", spaced); rc != 0 {
		t.Errorf("exit %d — the path was fragmented at its spaces", rc)
	}

	if rc := runValidate("/usr/bin/false", "ignored"); rc == 0 {
		t.Error("a failing validator reported success")
	}
	// A template naming a binary that does not exist is a failure, not a pass:
	// treating "could not run the check" as "the check passed" is how an
	// unvalidated edit ships.
	if rc := runValidate("definitely-not-installed-anywhere", "x"); rc == 0 {
		t.Error("a missing validator binary was treated as a pass")
	}
	// An empty template means no validator is configured, which is a pass.
	if rc := runValidate("", "x"); rc != 0 {
		t.Errorf("an empty template returned %d, want 0 (no validator configured)", rc)
	}
}

func TestReadFileAndFileExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")

	if content, existed := readFile(path); existed || content != "" {
		t.Errorf("readFile of a missing file = (%q, %v)", content, existed)
	}
	if fileExists(path) {
		t.Error("fileExists = true for a missing file")
	}

	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if content, existed := readFile(path); !existed || content != "hello" {
		t.Errorf("readFile = (%q, %v), want (hello, true)", content, existed)
	}
	if !fileExists(path) {
		t.Error("fileExists = false for a file that exists")
	}
	// An empty file exists — distinguishing it from a missing one is what
	// decides "create" versus "modify" in the edit prompt.
	empty := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if content, existed := readFile(empty); !existed || content != "" {
		t.Errorf("readFile of an empty file = (%q, %v), want (\"\", true)", content, existed)
	}
}

func TestEnumToTier_MatchesTheRouter(t *testing.T) {
	// Same mapping the router applies, or a fan-out routes differently from a
	// single dispatch of the same enum.
	for _, enum := range []string{"SIMPLE", "STANDARD", "COMPLEX", "GRUNT", ""} {
		if got, want := enumToTier(enum), dispatch.EnumToTier(enum); got != want {
			t.Errorf("enumToTier(%q) = %q but the router says %q", enum, got, want)
		}
	}
}

func TestDiffStats_CountsFromTheBackupWhenGitIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.txt")
	backup := file + ".hydra-bak"

	if err := os.WriteFile(backup, []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	added, removed := diffStats(file, "one\n", "", backup, true)
	if added != 2 || removed != 0 {
		t.Errorf("diffStats = (%d, %d), want (2, 0) — counted from the backup, not "+
			"by re-parsing diff(1) (#260)", added, removed)
	}

	// With no backup at all it falls back to a line-count delta, which must
	// still report growth rather than zero.
	noBackup := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(noBackup, []byte("a\nb\nc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	added, removed = diffStats(noBackup, "a\n", "", noBackup+".hydra-bak", true)
	if added == 0 && removed == 0 {
		t.Error("diffStats reported no change for a file that grew by two lines")
	}
}

func TestRollback_RestoresFromEachSource(t *testing.T) {
	t.Run("from the backup", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "a.txt")
		backup := file + ".hydra-bak"
		if err := os.WriteFile(backup, []byte("original\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("bad edit\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		rollback(file, "original\n", true, "", backup)

		if raw, _ := os.ReadFile(file); string(raw) != "original\n" {
			t.Errorf("file = %q after rollback", raw)
		}
		if _, err := os.Stat(backup); err == nil {
			t.Error("the backup survived; the next edit would see a stale baseline")
		}
	})

	t.Run("a file that did not exist is removed", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "created.txt")
		if err := os.WriteFile(file, []byte("bad edit\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		rollback(file, "", false, "", file+".hydra-bak")

		if _, err := os.Stat(file); err == nil {
			t.Error("a file the edit created was left behind after rollback")
		}
	})

	t.Run("from the in-memory snapshot", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "a.txt")
		if err := os.WriteFile(file, []byte("bad edit\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		rollback(file, "original\n", true, "", file+".hydra-bak")

		if raw, _ := os.ReadFile(file); string(raw) != "original\n" {
			t.Errorf("file = %q, want the snapshot restored", raw)
		}
	})
}

func boolPtr(b bool) *bool { return &b }

// writeWorkspaceYAML points $HYDRA_HOME's registry at a workspace rooted here,
// with a top-level validator for .go files. allowed_globs is required: a
// workspace without it matches no file at all.
func writeWorkspaceYAML(t *testing.T, root, goValidator string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("HYDRA_HOME"), "registry")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "version: \"1.0\"\nworkspaces:\n  test:\n    root: " + root +
		"\n    git: \"false\"\n    allowed_globs: [\"**\"]\nvalidators:\n  go: \"" +
		goValidator + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "workspace.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Replaces the two single-case tests that were in parallel_test.go: these are
// strict supersets of them, and two partial tests of one function is how a
// shape goes uncovered while looking covered.
//
// extractContent handles three shapes the model can produce: both markers, a
// terminator with no opener (the model started writing straight away), and an
// opener with no terminator (it ran out of budget mid-answer).
func TestExtractContent_EveryMarkerShape(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{{
		name: "both markers",
		raw:  "preamble\n<<<HYDRA_FILE_START>>>\nline one\nline two\n<<<HYDRA_FILE_END>>>\ntrailing prose",
		want: "line one\nline two",
	}, {
		// The model skipped the opener and wrote the file directly. Everything
		// up to the terminator is the content.
		name: "terminator only",
		raw:  "line one\nline two\n<<<HYDRA_FILE_END>>>",
		want: "line one\nline two",
	}, {
		// Truncated mid-answer. Everything after the opener is what there is.
		name: "opener only",
		raw:  "<<<HYDRA_FILE_START>>>\nline one\nline two",
		want: "line one\nline two",
	}, {
		// Neither marker: the model answered in prose. Returning "" is what
		// makes runEditTask refuse rather than write the prose to the file.
		name: "no markers at all",
		raw:  "Sure, here is what I would do...",
		want: "",
	}, {
		name: "empty input",
		raw:  "",
		want: "",
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractContent(tt.raw); got != tt.want {
				t.Errorf("extractContent = %q, want %q", got, tt.want)
			}
		})
	}
}

// Models wrap output in a code fence even when told not to. The fence must come
// off, but a fence *inside* the file (a markdown file, a README) must not.
func TestStripOuterFence(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain fence", "```\ncode\n```", "code"},
		{"language-tagged fence", "```go\ncode\n```", "code"},
		{"no fence", "code", "code"},
		{"opening fence only", "```go\ncode", "code"},
		{
			// An inner fence belongs to the file's own content.
			name: "inner fences survive",
			in:   "```md\n# Title\n\n```sh\nrun me\n```\n```",
			want: "# Title\n\n```sh\nrun me\n```",
		},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripOuterFence(tt.in); got != tt.want {
				t.Errorf("stripOuterFence(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// tscTemplate is only offered when the workspace actually has tsc installed —
// naming a binary that is not there would fail every TypeScript edit at the
// validation step and roll back a correct change.
func TestTSCTemplate_OnlyWhenTSCIsInstalled(t *testing.T) {
	root := t.TempDir()

	if got := tscTemplate(root); got != "" {
		t.Errorf("tscTemplate = %q with no node_modules, want empty", got)
	}

	bin := filepath.Join(root, "node_modules", ".bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "tsc"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// No tsconfig.json: check the single file with explicit flags.
	got := tscTemplate(root)
	if !strings.Contains(got, "--noEmit") || !strings.Contains(got, "{file}") {
		t.Errorf("tscTemplate = %q, want a single-file check naming {file}", got)
	}

	// With a tsconfig.json the project's own settings win, and the template
	// checks the project rather than one file.
	if err := os.WriteFile(filepath.Join(root, "tsconfig.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	got = tscTemplate(root)
	if !strings.Contains(got, "-p ") {
		t.Errorf("tscTemplate = %q, want it to use the project's tsconfig.json", got)
	}
	if strings.Contains(got, "{file}") {
		t.Errorf("tscTemplate = %q — a project-wide check takes no file argument", got)
	}
}

// persistResults must survive a logs directory it cannot write to, since it
// runs after the edits have already landed on disk. Failing here must not be
// mistaken for the batch failing.
func TestPersistResults_UnwritableLogDirIsReportedNotFatal(t *testing.T) {
	testutil.NewSandbox(t)

	// Dir() is a regular file, so logs/ cannot be created. The sandbox
	// pre-creates it as an empty directory, so remove that first.
	if err := os.RemoveAll(config.Dir()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Dir(), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := persistResults([]Result{{raw: json.RawMessage(`{"status":"ok"}`)}})
	if err == nil {
		t.Error("persistResults reported success with an uncreatable logs directory")
	}
}

// In a real repository git holds the baseline, so both the rollback and the
// line counts come from it rather than from a .hydra-bak that was never written.
func TestRollbackAndDiffStats_UseGitInARepository(t *testing.T) {
	s := testutil.NewSandbox(t)
	if !s.AllowHostBinary(t, "git") {
		t.Skip("git is not installed on this machine")
	}

	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "t@example.com")
	git("config", "user.name", "t")
	// Windows git defaults core.autocrlf=true, so a checkout rewrites LF to
	// CRLF and the restored bytes differ from the committed ones.
	git("config", "core.autocrlf", "false")

	file := filepath.Join(repo, "a.go")
	original := "one\n"
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "a.go")
	git("commit", "-qm", "init")

	// diffStats reads git's own numstat.
	if err := os.WriteFile(file, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	added, removed := diffStats(file, original, repo, file+".hydra-bak", true)
	if added != 2 || removed != 0 {
		t.Errorf("diffStats = (%d, %d), want (2, 0) from git numstat", added, removed)
	}

	// rollback restores from the index, not from a backup that does not exist.
	rollback(file, original, true, repo, file+".hydra-bak")
	if raw, _ := os.ReadFile(file); string(raw) != original {
		t.Errorf("file = %q after rollback, want the committed content", raw)
	}

	// An untracked file has nothing in the index, so rollback removes it.
	untracked := filepath.Join(repo, "new.go")
	if err := os.WriteFile(untracked, []byte("created by the edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rollback(untracked, "", false, repo, untracked+".hydra-bak")
	if _, err := os.Stat(untracked); err == nil {
		t.Error("an untracked file the edit created survived rollback")
	}

	// And its line count comes from the file itself, since git has no baseline
	// for it — reporting 0/0 would read as "nothing changed".
	fresh := filepath.Join(repo, "fresh.go")
	if err := os.WriteFile(fresh, []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if a, r := diffStats(fresh, "", repo, fresh+".hydra-bak", false); a == 0 && r == 0 {
		t.Error("a new file reported no lines added")
	}
}

// persistResults writes the batch for `hyctl review` to read. A batch whose
// results cannot be persisted must say so — the edits are already on disk, and
// the user needs to know they cannot see what changed.
func TestPersistResults_RoundTripsThroughDisk(t *testing.T) {
	testutil.NewSandbox(t)

	rows := []Result{
		{raw: json.RawMessage(`{"label":"one","mode":"edit","status":"ok","file":"/a.go"}`)},
		{raw: json.RawMessage(`{"label":"two","mode":"edit","status":"fail","file":"/b.go"}`)},
	}
	if err := persistResults(rows); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(config.Dir(), "logs", "last_parallel.json"))
	if err != nil {
		t.Fatalf("nothing was persisted: %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("last_parallel.json is not a JSON array: %v\n%s", err, raw)
	}
	if len(got) != 2 {
		t.Fatalf("persisted %d rows, want 2", len(got))
	}
	// Both outcomes survive: `hyctl review` needs the failures as much as the
	// successes, since a failed edit may still have touched the file.
	if got[0]["status"] != "ok" || got[1]["status"] != "fail" {
		t.Errorf("statuses did not round-trip: %v", got)
	}
}
