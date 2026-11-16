// SPDX-License-Identifier: MIT

package checkgen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/sandbox"
	"github.com/ankit373/hydra/internal/trust"
	"github.com/ankit373/hydra/internal/util"
)

const testsPrompt = "The request below changes the Go file that follows it. Write Go tests for the changed behaviour: " +
	"a few unit tests that cover its edge cases and one property test (testing/quick or a loop over many inputs). " +
	"Each test is a top-level func TestXxx(t *testing.T) in the file's own package. Use only the standard library " +
	"and the package itself; no network, no files outside t.TempDir(). Reply with a JSON array of strings, each " +
	"one complete _test.go file.\n\n"

// testsWall and testsCPUSeconds bound one go test run of the generated tests.
const (
	testsWall       = 2 * time.Minute
	testsCPUSeconds = 300
)

var testFuncName = regexp.MustCompile(`(?m)^func (Test\w*)\(\s*\w+ \*testing\.T\s*\)`)

type testsBody struct {
	File  string   `json:"file"`
	Tests []string `json:"tests"`
}

// testsGen writes Go tests for the file under change and runs them on a copy
// of its module, before and after the change, with the network off.
type testsGen struct{}

func init() {
	if err := Register("code", testsGen{}); err != nil {
		panic(err)
	}
}

func (testsGen) Kind() string { return "tests" }

func (testsGen) Generate(ctx context.Context, task Task) ([]Spec, error) {
	if task.File == "" {
		return nil, errors.New("no file under change: name it with --check gen:tests=<file>")
	}
	if filepath.Ext(task.File) != ".go" {
		return nil, fmt.Errorf("generated tests cover Go only for now, so %s is not checked", filepath.Base(task.File))
	}
	if task.Ask == nil {
		return nil, errors.New("checkgen tests: no writer head to ask")
	}
	src, err := os.ReadFile(task.File)
	if err != nil {
		return nil, err
	}
	reply, err := task.Ask(ctx, testsPrompt+util.WrapUntrusted("REQUEST", task.Text)+"\n"+
		util.WrapUntrusted("FILE "+filepath.Base(task.File), string(src)))
	if err != nil {
		return nil, err
	}
	var files []string
	start, end := strings.Index(reply, "["), strings.LastIndex(reply, "]")
	if start < 0 || end < start || json.Unmarshal([]byte(reply[start:end+1]), &files) != nil {
		return nil, fmt.Errorf("checkgen tests: writer did not reply with a JSON list: %.80q", reply)
	}
	files = slices.DeleteFunc(files, func(f string) bool { return !testFuncName.MatchString(f) })
	if len(files) == 0 {
		return nil, nil
	}
	s, err := NewSpec("tests", "", testsBody{File: task.File, Tests: files}, task, task.Writer)
	if err != nil {
		return nil, err
	}
	return []Spec{s}, nil
}

func (testsGen) Oracle(s Spec, _ Env) (oracle.Oracle, error) {
	var b testsBody
	if err := json.Unmarshal(s.Body, &b); err != nil {
		return nil, fmt.Errorf("checkgen tests: spec %s: %w", s.ID, err)
	}
	return testsOracle{body: b, wall: testsWall}, nil
}

type testsOracle struct {
	body testsBody
	wall time.Duration
}

// Verify passes when every counted test passes on the changed file. A test
// that fails before the change and after it is suspect and not counted.
func (o testsOracle) Verify(ctx context.Context, candidate string, _ trust.Task) (oracle.Verdict, error) {
	code, err := changedFile(candidate)
	if err != nil {
		return oracle.Verdict{}, err
	}
	if err := sandbox.NetworkIsolation(); err != nil {
		return oracle.Verdict{}, err
	}
	root := goModRoot(o.body.File)
	if root == "" {
		return oracle.Verdict{}, fmt.Errorf("%s is not in a Go module", o.body.File)
	}
	rel, err := filepath.Rel(root, o.body.File)
	if err != nil {
		return oracle.Verdict{}, err
	}
	var names []string
	for _, f := range o.body.Tests {
		for _, m := range testFuncName.FindAllStringSubmatch(f, -1) {
			names = append(names, m[1])
		}
	}
	slices.Sort(names)
	names = slices.Compact(names)

	copyDir, err := os.MkdirTemp("", "hydra-gen-tests-*")
	if err != nil {
		return oracle.Verdict{}, err
	}
	defer os.RemoveAll(copyDir)
	if err := copyRegularFiles(root, copyDir); err != nil {
		return oracle.Verdict{}, fmt.Errorf("copy %s: %w", root, err)
	}
	target := filepath.Join(copyDir, rel)
	for i, f := range o.body.Tests {
		name := filepath.Join(filepath.Dir(target), fmt.Sprintf("hydra_gen_%d_test.go", i))
		if err := os.WriteFile(name, []byte(f), 0o600); err != nil {
			return oracle.Verdict{}, err
		}
	}
	before, err := o.goTest(ctx, filepath.Dir(target), names)
	if err != nil {
		return oracle.Verdict{}, err
	}
	if err := os.WriteFile(target, []byte(code), 0o600); err != nil {
		return oracle.Verdict{}, err
	}
	after, err := o.goTest(ctx, filepath.Dir(target), names)
	if err != nil {
		return oracle.Verdict{}, err
	}
	return tally(names, before, after)
}

// tally counts the tests that passed at least once and fails on any of them failing after.
func tally(names []string, before, after map[string]bool) (oracle.Verdict, error) {
	var failed []string
	counted, dropped := 0, 0
	for _, n := range names {
		if !before[n] && !after[n] {
			dropped++
			continue
		}
		counted++
		if !after[n] {
			failed = append(failed, n)
		}
	}
	suspect := fmt.Sprintf("%d dropped as suspect (failed before and after the change)", dropped)
	if counted == 0 {
		return oracle.Verdict{}, fmt.Errorf("no generated test passed before or after the change; %s", suspect)
	}
	if len(failed) > 0 {
		return oracle.Verdict{Detail: fmt.Sprintf("failed after the change: %s; %s",
			strings.Join(failed, ", "), suspect)}, nil
	}
	return oracle.Verdict{Passed: true, Detail: fmt.Sprintf("%d generated tests passed; %s", counted, suspect)}, nil
}

// goTest runs the named tests in dir with no network, no credentials and
// bounded time, and reports which passed. A test that never reported failed.
func (o testsOracle) goTest(ctx context.Context, dir string, names []string) (map[string]bool, error) {
	run, cancel := context.WithTimeout(ctx, o.wall)
	defer cancel()
	cmd := sandbox.Confine(exec.CommandContext(run, "go", "test", "-count=1", "-json",
		"-run", "^("+strings.Join(names, "|")+")$", "."), nil)
	cmd.Dir = dir
	// Offline, a module fetch can only fail; GOPROXY=off makes it fail now.
	cmd.Env = append(cmd.Env, "GOPROXY=off")
	if err := sandbox.WithLimits(cmd, 0, testsCPUSeconds); err != nil {
		return nil, err
	}
	if err := sandbox.Offline(cmd); err != nil {
		return nil, err
	}
	out, err := cmd.Output()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	passed := map[string]bool{}
	for _, line := range bytes.Split(out, []byte("\n")) {
		var ev struct{ Action, Test string }
		if json.Unmarshal(line, &ev) == nil && ev.Test != "" && (ev.Action == "pass" || ev.Action == "fail") {
			passed[ev.Test] = ev.Action == "pass"
		}
	}
	return passed, nil
}

// changedFile is the answer as file content: one fenced block, or the whole
// answer when it has no fence at all.
func changedFile(answer string) (string, error) {
	if block := util.SoleFencedBlock(answer); block != "" {
		lines := util.SplitLines(block)
		return strings.Join(lines[1:len(lines)-1], "\n") + "\n", nil
	}
	if strings.Contains(answer, "```") {
		return "", errors.New("the answer is not one file: want a single fenced block or bare code")
	}
	return answer, nil
}

// goModRoot is the directory of the go.mod that owns file, or "".
func goModRoot(file string) string {
	for dir := filepath.Dir(file); ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		if filepath.Dir(dir) == dir {
			return ""
		}
	}
}

// copyRegularFiles copies the tree under src into dst, skipping .git and every
// symlink, so nothing written in the copy can reach the user's tree.
func copyRegularFiles(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		switch {
		case d.IsDir() && d.Name() == ".git":
			return filepath.SkipDir
		case d.IsDir():
			return os.MkdirAll(filepath.Join(dst, rel), 0o700)
		case !d.Type().IsRegular():
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), raw, 0o600)
	})
}
