// SPDX-License-Identifier: MIT

package editor

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
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/testutil"

	// Providers register themselves in init(), and this package does not import
	// them — without these blanks provider.All() is empty in the test binary and
	// every dispatch finds no heads. cmd/hydra imports the same set.
	_ "github.com/ankit373/hydra/internal/provider/cli"
)

// Edit is `hyctl edit`: one scoped, validated, rollback-safe file edit driven
// by a model's output. Nothing about it was covered, and every guard on it is
// the difference between an edit and a lost file.

// editSandbox gives a hermetic environment with a config, a workspace rooted at
// a temp repo, and a fake head on PATH that replies with the given body — so a
// real end-to-end edit runs with no network and no API key.
func editSandbox(t *testing.T, reply string) string {
	t.Helper()
	s := testutil.NewSandbox(t)

	if err := config.Save(&config.Config{Cortex: "cody"}); err != nil {
		t.Fatal(err)
	}

	// `cody` rather than `claude`: its capability score puts it at UITier 6, so
	// enum MODERATE routes to it. A tier-1 head is stronger than any enum an
	// edit may ask for, and selection would find nothing.
	//
	// /bin/cat by absolute path — the sandbox empties $PATH.
	body := "#!/bin/sh\n/bin/cat <<'HYDRA_EOF'\n" + reply + "\nHYDRA_EOF\n"
	if runtime.GOOS == "windows" {
		body = "@echo off\r\n"
		for _, line := range strings.Split(reply, "\n") {
			if line == "" {
				body += "echo.\r\n"
				continue
			}
			body += "echo " + line + "\r\n"
		}
	}
	s.FakeBinary(t, "cody", body)

	repo := t.TempDir()
	// A .git directory that is not a repository: workspace resolution roots
	// here, but git commands fail, so the .hydra-bak backup is what protects
	// the file — the path a non-git user is on.
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

	cwd, err := os.Getwd()
	if err != nil {
		return repo
	}
	return cwd
}

func marked(content string) string {
	return "<<<HYDRA_FILE_START>>>\n" + content + "\n<<<HYDRA_FILE_END>>>"
}

func TestEdit_WritesTheContentAndRecordsTheEdit(t *testing.T) {
	repo := editSandbox(t, marked("package main\n\nfunc main() {}"))
	file := filepath.Join(repo, "main.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Edit(context.Background(), Request{
		File: file, Enum: "MODERATE", Prompt: "add an empty main", RunID: "run-edit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, error %q", res.Status, res.Error)
	}

	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "func main() {}") {
		t.Errorf("file = %q, want the model's content", raw)
	}
	if strings.Contains(string(raw), "HYDRA_FILE") {
		t.Errorf("the markers leaked into the file: %q", raw)
	}
	if res.LinesAdded == 0 {
		t.Errorf("LinesAdded = 0 for a file that grew: %+v", res)
	}

	// last_edit.json is what `hyctl review` with no arguments reads.
	lastEdit, err := os.ReadFile(filepath.Join(config.Dir(), "logs", "last_edit.json"))
	if err != nil {
		t.Fatalf("no last_edit.json was written, so `hyctl review` sees nothing: %v", err)
	}
	var entry map[string]any
	if err := json.Unmarshal(lastEdit, &entry); err != nil {
		t.Fatal(err)
	}
	if entry["file"] != file || entry["enum"] != "MODERATE" {
		t.Errorf("last_edit.json = %v", entry)
	}

	// The run log records that the file changed, with the before/after held
	// beside the log rather than inlined in the event.
	events, err := runlog.Load("run-edit")
	if err != nil {
		t.Fatal(err)
	}
	var sawEdit bool
	for _, e := range events {
		if e.Kind == runlog.KindEdit {
			sawEdit = true
			if e.Ref == "" {
				t.Error("the edit event carries no snapshot ref, so the diff cannot be rendered")
			}
			if !strings.Contains(e.Detail, "+") {
				t.Errorf("edit event Detail = %q, want the line counts", e.Detail)
			}
		}
	}
	if !sawEdit {
		t.Errorf("no edit event in the run log: %+v", events)
	}
}

func TestEdit_CreatesAFileThatDoesNotExistYet(t *testing.T) {
	repo := editSandbox(t, marked("brand new"))
	file := filepath.Join(repo, "sub", "new.txt")

	res, err := Edit(context.Background(), Request{
		File: file, Enum: "MODERATE", Prompt: "create it",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, error %q", res.Status, res.Error)
	}
	if raw, err := os.ReadFile(file); err != nil || !strings.Contains(string(raw), "brand new") {
		t.Errorf("file = %q, err %v", raw, err)
	}
	// Nothing to back up for a file that did not exist, so no stray .hydra-bak.
	if _, err := os.Stat(file + ".hydra-bak"); err == nil {
		t.Error("a backup was created for a file that did not exist")
	}
}

// An empty replacement must leave the file byte-identical. This is the worst
// outcome the edit path can produce.
func TestEdit_EmptyReplacementLeavesTheFileUntouched(t *testing.T) {
	repo := editSandbox(t, marked(""))
	file := filepath.Join(repo, "keep.go")
	original := "package main\n\n// do not lose me\n"
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Edit(context.Background(), Request{
		File: file, Enum: "MODERATE", Prompt: "empty it",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "fail" {
		t.Fatalf("status = %q, want fail", res.Status)
	}
	if raw, _ := os.ReadFile(file); string(raw) != original {
		t.Errorf("the file was modified despite the refusal: %q", raw)
	}
	// The backup this edit created must not be left behind — a stale one is a
	// wrong baseline for the next edit and for `hyctl review`.
	if _, err := os.Stat(file + ".hydra-bak"); err == nil {
		t.Error("a backup survived a refused edit")
	}
}

func TestEdit_RefusesBeforeDispatching(t *testing.T) {
	repo := editSandbox(t, marked("x"))

	tests := []struct {
		name    string
		req     Request
		wantErr string
	}{
		{"relative path", Request{File: "relative.go", Enum: "MODERATE", Prompt: "x"}, "absolute"},
		{"CORE is the orchestrator's own job",
			Request{File: filepath.Join(repo, "a.go"), Enum: "CORE", Prompt: "x"}, "CORE"},
		{"outside every workspace",
			Request{File: filepath.Join(t.TempDir(), "elsewhere.go"), Enum: "MODERATE", Prompt: "x"},
			"scope_rejected"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := Edit(context.Background(), tt.req)
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != "fail" {
				t.Fatalf("status = %q, want fail", res.Status)
			}
			if !strings.Contains(res.Error, tt.wantErr) {
				t.Errorf("Error = %q, want it to mention %q", res.Error, tt.wantErr)
			}
		})
	}
}

// A validator that rejects the edit rolls the file back. An edit that fails
// validation and stays on disk is worse than no edit at all.
func TestEdit_FailedValidationRollsBack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the workspace validator template is a POSIX command line here")
	}
	repo := editSandbox(t, marked("will not validate"))
	writeWorkspaceYAML(t, repo, "/usr/bin/false {file}")

	file := filepath.Join(repo, "a.go")
	original := "package main\n"
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Edit(context.Background(), Request{
		File: file, Enum: "MODERATE", Prompt: "x", Validate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "fail" || !res.RolledBack {
		t.Fatalf("result = %+v, want a rolled-back failure", res)
	}
	if raw, _ := os.ReadFile(file); string(raw) != original {
		t.Errorf("the file was left in its failed state: %q", raw)
	}
	if _, err := os.Stat(file + ".hydra-bak"); err == nil {
		t.Error("the backup survived the rollback")
	}
}

func TestEdit_PassingValidationKeepsTheEdit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the workspace validator template is a POSIX command line here")
	}
	repo := editSandbox(t, marked("validated"))
	writeWorkspaceYAML(t, repo, "/usr/bin/true {file}")

	file := filepath.Join(repo, "a.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Edit(context.Background(), Request{
		File: file, Enum: "MODERATE", Prompt: "x", Validate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "ok" || !res.ValidatorPassed {
		t.Fatalf("result = %+v", res)
	}
	if raw, _ := os.ReadFile(file); !strings.Contains(string(raw), "validated") {
		t.Errorf("the edit was rolled back despite passing: %q", raw)
	}
}

// The backup is a verbatim copy of the user's source sitting beside it until
// the edit is approved. It must not be world-readable — the same defect #273
// fixed for the run log's edit snapshots.
//
// It is only created for a non-git workspace: with a git root, git itself holds
// the baseline. So the workspace is declared git:"false" here, which is the
// path a user outside a repository is on.
func TestEdit_BackupIsCreatedForNonGitWorkspacesAndIsNotWorldReadable(t *testing.T) {
	repo := editSandbox(t, marked("new content"))
	writeWorkspaceYAML(t, repo, "")

	file := filepath.Join(repo, "secretish.go")
	if err := os.WriteFile(file, []byte("const apiKey = \"sk-live\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Edit(context.Background(), Request{File: file, Enum: "MODERATE", Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "ok" {
		t.Fatalf("result = %+v", res)
	}
	if res.GitRoot != "" {
		t.Fatalf("GitRoot = %q; the workspace declares git:false, so the backup "+
			"path is what should have run", res.GitRoot)
	}

	info, err := os.Stat(file + ".hydra-bak")
	if err != nil {
		t.Fatalf("no backup was kept after a successful edit, so `hyctl review "+
			"reject` has no baseline to restore: %v", err)
	}
	if runtime.GOOS == "windows" {
		return // mode bits carry no access information here
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("backup mode %v is group/other readable; it is a verbatim copy of "+
			"the user's source", info.Mode().Perm())
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// atomicWrite must preserve the file's existing mode. An edit that silently
// re-permissions a script to 0644 breaks it, and one that leaves a temp file
// behind litters the user's tree on every failure.
func TestAtomicWrite_PreservesModeAndLeavesNoTempFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows does not carry the executable bit this way")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := atomicWrite(script, "#!/bin/sh\necho hi\n"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(script)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v after the edit, want 0755 — the script is no longer "+
			"executable", info.Mode().Perm())
	}

	// A new file gets the usual default rather than the temp file's 0600.
	fresh := filepath.Join(dir, "new.txt")
	if err := atomicWrite(fresh, "hello"); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("new file mode = %v, want 0644", info.Mode().Perm())
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".hydra-tmp") {
			t.Errorf("%s survived a successful write", e.Name())
		}
	}
}

func TestReadFile_DistinguishesEmptyFromMissing(t *testing.T) {
	dir := t.TempDir()

	if content, existed := readFile(filepath.Join(dir, "absent")); existed || content != "" {
		t.Errorf("readFile of a missing file = (%q, %v)", content, existed)
	}
	// An empty file exists. That distinction decides "create" versus "modify"
	// in the prompt the model is given.
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if content, existed := readFile(empty); !existed || content != "" {
		t.Errorf("readFile of an empty file = (%q, %v), want (\"\", true)", content, existed)
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
		if err := os.WriteFile(file, []byte("bad\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		rollback(file, "original\n", true, "", backup)

		if raw, _ := os.ReadFile(file); string(raw) != "original\n" {
			t.Errorf("file = %q after rollback", raw)
		}
		if _, err := os.Stat(backup); err == nil {
			t.Error("the backup survived, so the next edit sees a stale baseline")
		}
	})

	t.Run("a created file is removed", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "created.txt")
		if err := os.WriteFile(file, []byte("bad\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		rollback(file, "", false, "", file+".hydra-bak")
		if _, err := os.Stat(file); err == nil {
			t.Error("a file the edit created was left behind")
		}
	})

	t.Run("from the in-memory snapshot", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "a.txt")
		if err := os.WriteFile(file, []byte("bad\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		rollback(file, "original\n", true, "", file+".hydra-bak")
		if raw, _ := os.ReadFile(file); string(raw) != "original\n" {
			t.Errorf("file = %q, want the snapshot restored", raw)
		}
	})
}

func TestTSCTemplate_OnlyWhenTSCIsInstalled(t *testing.T) {
	root := t.TempDir()
	if got := tscTemplate(root); got != "" {
		t.Errorf("tscTemplate = %q with no node_modules, want empty — naming a "+
			"binary that is not there fails every TypeScript edit at validation "+
			"and rolls back a correct change", got)
	}

	bin := filepath.Join(root, "node_modules", ".bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "tsc"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := tscTemplate(root); !strings.Contains(got, "--noEmit") {
		t.Errorf("tscTemplate = %q, want a type-check", got)
	}

	if err := os.WriteFile(filepath.Join(root, "tsconfig.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := tscTemplate(root); !strings.Contains(got, "-p ") {
		t.Errorf("tscTemplate = %q, want it to use the project's tsconfig.json", got)
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
		t.Errorf("diffStats = (%d, %d), want (2, 0) — counted from the edit script, "+
			"not by re-parsing diff(1) (#260)", added, removed)
	}
}

func TestWriteLastEdit_IsReadableByReview(t *testing.T) {
	testutil.NewSandbox(t)

	if err := writeLastEdit("/abs/file.go", "SIMPLE", "ws", 3, 1); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(config.Dir(), "logs", "last_edit.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	// `hyctl review` with no arguments reads the "file" key out of this.
	if got["file"] != "/abs/file.go" {
		t.Errorf("last_edit.json file = %v", got["file"])
	}
	if got["lines_added"] != float64(3) || got["lines_removed"] != float64(1) {
		t.Errorf("line counts = %v/%v", got["lines_added"], got["lines_removed"])
	}
}

// writeWorkspaceYAML points $HYDRA_HOME's registry at a workspace rooted here
// with a validator for .go files. allowed_globs is required: a workspace
// without it matches no file at all.
func writeWorkspaceYAML(t *testing.T, root, goValidator string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("HYDRA_HOME"), "registry")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "version: \"1.0\"\nworkspaces:\n  test:\n    root: " + root +
		"\n    git: \"false\"\n    allowed_globs: [\"**\"]\n"
	if goValidator != "" {
		body += "validators:\n  go: \"" + goValidator + "\"\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "workspace.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// atomicWrite's remaining paths: an unreadable source mode falls back to the
// default rather than erroring, and a write that cannot rename leaves nothing
// behind.
func TestAtomicWrite_OverwritesAndPreservesContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")

	if err := atomicWrite(path, "first"); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(path, "second"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "second" {
		t.Errorf("content = %q after overwrite, want second", raw)
	}

	// Content with no trailing newline, embedded NULs and CRLF must round-trip
	// byte for byte — atomicWrite is not a text transform.
	tricky := "no newline at end\r\nwith crlf\x00and a nul"
	if err := atomicWrite(path, tricky); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != tricky {
		t.Errorf("content = %q, want it byte-identical", raw)
	}
}

// diffStats' last resort is a line-count delta, used when there is neither a
// git root nor a backup. It must still report a change rather than 0/0, which
// reads as "nothing happened".
func TestDiffStats_LineCountFallback(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(file, []byte("a\nb\nc\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	added, removed := diffStats(file, "a\n", "", filepath.Join(dir, "absent.bak"), true)
	if added == 0 && removed == 0 {
		t.Error("diffStats reported no change for a file that grew by two lines")
	}

	// Shrinking is counted as removals, not as negative additions.
	if err := os.WriteFile(file, []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	added, removed = diffStats(file, "a\nb\nc\n", "", filepath.Join(dir, "absent.bak"), true)
	if removed == 0 {
		t.Errorf("diffStats = (%d, %d) for a file that shrank", added, removed)
	}
	if added < 0 || removed < 0 {
		t.Errorf("diffStats = (%d, %d); a negative count renders as a bogus number",
			added, removed)
	}
}

// stripOuterFence must not eat a fence that belongs to the file's own content.
func TestStripOuterFence_InnerFencesSurvive(t *testing.T) {
	in := "```md\n# Title\n\n```sh\nrun me\n```\n```"
	want := "# Title\n\n```sh\nrun me\n```"
	if got := stripOuterFence(in); got != want {
		t.Errorf("stripOuterFence = %q, want %q", got, want)
	}
	if got := stripOuterFence(""); got != "" {
		t.Errorf("stripOuterFence(\"\") = %q", got)
	}
	if got := stripOuterFence("```go\nunterminated"); got != "unterminated" {
		t.Errorf("stripOuterFence with no closing fence = %q", got)
	}
}

// A rename that cannot complete must leave no temp file. Windows refuses a
// rename onto a file another process has open, so this is a real collision
// path, not a hypothetical one — and every collision leaving a .hydra-tmp.*
// behind litters the user's source tree.
func TestAtomicWrite_FailedRenameLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "adirectory")
	if err := os.MkdirAll(filepath.Join(target, "child"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := atomicWrite(target, "content"); err == nil {
		t.Fatal("atomicWrite reported success onto a non-empty directory")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".hydra-tmp") {
			t.Errorf("%s survived a failed rename", e.Name())
		}
	}
}

// In a real repository git holds the baseline, so a rollback restores from the
// index rather than from a .hydra-bak that was never written.
func TestRollback_UsesGitInARepository(t *testing.T) {
	s := testutil.NewSandbox(t)
	if !s.AllowHostBinary(t, "git") {
		t.Skip("git is not installed on this machine")
	}

	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
		// Windows git defaults core.autocrlf=true, so a checkout rewrites LF to
		// CRLF and the restored bytes differ from the committed ones.
		{"config", "core.autocrlf", "false"},
	} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}

	file := filepath.Join(repo, "a.go")
	original := "package main\n"
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "a.go"}, {"commit", "-qm", "init"}} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}

	if err := os.WriteFile(file, []byte("a bad edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rollback(file, original, true, repo, file+".hydra-bak")

	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != original {
		t.Errorf("file = %q after rollback, want the committed content", raw)
	}

	// An untracked file has nothing in the index to restore, so it is removed.
	untracked := filepath.Join(repo, "new.go")
	if err := os.WriteFile(untracked, []byte("created by the edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rollback(untracked, "", false, repo, untracked+".hydra-bak")
	if _, err := os.Stat(untracked); err == nil {
		t.Error("an untracked file the edit created survived the rollback")
	}
}
