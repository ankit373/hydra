// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/spf13/cobra"
)

// fencedAnswer is a reply that would rewrite any file, so a test can tell
// whether a head ran at all.
const fencedAnswer = "```\nafter\n```"

// editTarget writes a file holding "before\n" into repo and returns its path.
func editTarget(t *testing.T, repo string) string {
	t.Helper()
	file := filepath.Join(repo, "a.txt")
	if err := os.WriteFile(file, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

// assertUntouched fails when a head wrote file after all.
func assertUntouched(t *testing.T, file string) {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "before\n" {
		t.Errorf("a head ran and wrote the file: %q", raw)
	}
}

// A typo in --enum used to route unrestricted, to the most expensive head, and
// write the file. It is refused before any head runs, as dispatch refuses it (#1163).
func TestCLI_Edit_RefusesAnUnknownEnum(t *testing.T) {
	_, repo := dispatchable(t, fencedAnswer)
	file := editTarget(t, repo)

	_, _, err := run(t, "edit", "--file", file, "--enum", "SIMPL", "--prompt", "x", "--no-validate")
	if err == nil {
		t.Fatal("`hyctl edit --enum SIMPL` was accepted")
	}
	_, _, dispErr := run(t, "dispatch", "--enum", "SIMPL", "x")
	if dispErr == nil || err.Error() != dispErr.Error() {
		t.Errorf("edit refused with %q, dispatch with %v: want the same message", err, dispErr)
	}
	assertUntouched(t, file)
}

// `hyctl parallel` had the same hole, one enum per task.
func TestCLI_Parallel_RefusesAnUnknownEnum(t *testing.T) {
	_, repo := dispatchable(t, fencedAnswer)
	file := editTarget(t, repo)
	spec := filepath.Join(repo, "batch.json")
	body := `[
  {
    "label": "one",
    "enum": "SIMPL",
    "file": "` + filepath.ToSlash(file) + `",
    "prompt": "x",
    "validate": false
  }
]`
	if err := os.WriteFile(spec, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err := run(t, "parallel", "--tasks", spec)
	if err == nil || !strings.Contains(err.Error(), `unknown --enum "SIMPL"`) {
		t.Fatalf("err = %v, want the unknown enum refused", err)
	}
	assertUntouched(t, file)
}

// The fake head is a paid CLI agent, so --local leaves nothing to route to.
func TestCLI_Edit_LocalKeepsTheEditOffPaidHeads(t *testing.T) {
	_, repo := dispatchable(t, fencedAnswer)
	file := editTarget(t, repo)

	args := []string{"edit", "--file", file, "--enum", "MODERATE", "--prompt", "x"}
	out, _, err := run(t, append(args, "--no-validate", "--local")...)
	var status exitStatus
	if !errors.As(err, &status) || status != 2 {
		t.Fatalf("err = %v, want exit status 2 from a failed edit", err)
	}
	var res struct {
		Error string `json:"error"`
	}
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); jerr != nil {
		t.Fatalf("no JSON result: %v\n%s", jerr, out)
	}
	if !strings.HasPrefix(res.Error, "route_failed") {
		t.Errorf("error = %q, want route_failed for want of a local head", res.Error)
	}
	assertUntouched(t, file)
}

// os.Exit(2) skipped the deferred heartbeat stop and run close, so a failed
// edit read as live for ever (#1171). Only a real exit shows it.
func TestExitCodes_AFailedEditStillClosesItsRun(t *testing.T) {
	requireHyctl(t)
	s := cliSandbox(t)
	const runID = "edit-exit-defers"
	t.Setenv("HYDRA_RUN_ID", runID)

	code, out := runBinary(t, s, "edit", "--file", "relative.go", "--enum", "SIMPLE", "--prompt", "x")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2:\n%s", code, out)
	}
	events, err := runlog.Load(runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || events[len(events)-1].Kind != runlog.KindRunFinished {
		t.Errorf("the run was never closed: %+v", events)
	}
	_, statErr := os.Stat(runlog.HeartbeatPath(runID))
	if statErr == nil {
		t.Error("the heartbeat outlived the process, so the run reads as live")
	}
}

// --failed over a corpus of passes has nothing to show. It used to print the
// empty-corpus hint, which says the corpus does not exist (#1171).
func TestCLI_EvalListFailedOnAnAllPassCorpusPrintsNothing(t *testing.T) {
	cliSandbox(t)
	pass := evalset.Example{Domain: "go", Source: "oracle:test", Candidate: "package a", Passed: true}
	if _, err := evalset.Add(evalset.DefaultPath(), pass); err != nil {
		t.Fatal(err)
	}

	out, _, err := run(t, "eval", "list", "--failed")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("printed %q, want nothing", out)
	}

	out, _, err = run(t, "eval", "list", "--failed", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("--json printed %q, want []", out)
	}
}

// Every `hyctl edit` the docs show has to get past flag parsing. They showed a
// positional prompt, no --enum and relative paths, all refused (#1171).
func TestDocs_EveryHyctlEditInvocationRuns(t *testing.T) {
	htmlTag := regexp.MustCompile(`</?(span|pre|div|code)[^>]*>`)
	docsHTML := htmlTag.ReplaceAllString(repoFile(t, "docs", "docs.html"), "")
	sources := map[string]string{
		"README.md":      repoFile(t, "README.md"),
		"CLAUDE.md":      repoFile(t, "CLAUDE.md"),
		"docs/docs.html": docsHTML,
		"Code.tsx":       repoFile(t, "desktop", "frontend", "src", "views", "Code.tsx"),
		"main.go":        repoFile(t, "cmd", "hydra", "main.go"),
	}
	invocation := regexp.MustCompile("hyctl edit (--[^`#\n]*)")
	placeholder := regexp.MustCompile(`^(…|\.\.\.|<[^>]*>)$`)

	found := 0
	for name, text := range sources {
		for _, m := range invocation.FindAllStringSubmatch(text, -1) {
			found++
			line := strings.TrimSpace(strings.Split(m[1], "</code>")[0])
			args := shellWords(strings.ReplaceAll(line, `\"`, `"`))
			c, err := parseEdit(args)
			if err != nil {
				t.Errorf("%s: `hyctl edit %s` is refused: %v", name, line, err)
				continue
			}
			file, _ := c.Flags().GetString("file")
			absolute := filepath.IsAbs(file) || strings.HasPrefix(file, "$PWD/")
			if !placeholder.MatchString(file) && !absolute {
				t.Errorf("%s: `hyctl edit %s` names a relative --file, which edit refuses", name, line)
			}
			enum, _ := c.Flags().GetString("enum")
			if !placeholder.MatchString(enum) && !dispatch.IsKnownEnum(enum) {
				t.Errorf("%s: `hyctl edit %s` names an unknown --enum", name, line)
			}
		}
	}
	if found == 0 {
		t.Fatal("no documented `hyctl edit` invocation found; the pattern no longer matches the docs")
	}
}

// parseEdit runs cobra's own checks on args: flags, required flags, positionals.
func parseEdit(args []string) (*cobra.Command, error) {
	c := cmdEdit()
	if err := c.ParseFlags(args); err != nil {
		return nil, err
	}
	if err := c.ValidateRequiredFlags(); err != nil {
		return nil, err
	}
	return c, c.ValidateArgs(c.Flags().Args())
}

// shellWords splits on spaces outside double quotes, enough for a doc line.
func shellWords(s string) []string {
	var out []string
	var cur strings.Builder
	quoted, have := false, false
	for _, r := range s {
		switch {
		case r == '"':
			quoted, have = !quoted, true
		case r == ' ' && !quoted:
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteRune(r)
			have = true
		}
	}
	if have {
		out = append(out, cur.String())
	}
	return out
}
