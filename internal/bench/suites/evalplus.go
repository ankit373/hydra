// SPDX-License-Identifier: MIT

package suites

import (
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/bench"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/trust"
)

//go:embed evalplus_runner.py
var evalplusRunner []byte

const (
	evalplusAnswerTokens = 1024
	// evalplusDeadline bounds one grade. The runner's per-input limits fire first,
	// unless the solution keeps Python from ever running its alarm handler.
	evalplusDeadline = 2 * time.Minute
	evalplusMemMB    = 2048
	evalplusCPUSecs  = 120
)

var codeBlockRe = regexp.MustCompile("(?s)```(?:python|py)?[ \t]*\n(.*?)```")

type evalplusTask struct {
	Prompt     string          `json:"prompt"`
	EntryPoint string          `json:"entry_point"`
	Canonical  string          `json:"canonical_solution"`
	Base       json.RawMessage `json:"base_input"`
	Plus       json.RawMessage `json:"plus_input"`
	Atol       float64         `json:"atol"`
}

// evalplus is one EvalPlus dataset. Grade runs the base and plus inputs, as EvalPlus
// scores "plus"; BaseTests runs the original tests alone, the cheap check.
type evalplus struct {
	name, file string
	deadline   time.Duration
	tasks      map[string]evalplusTask
}

func (s *evalplus) Name() string     { return s.name }
func (s *evalplus) Class() string    { return "code." + s.name }
func (s *evalplus) Labels() []string { return nil }

func (s *evalplus) Rows(ds bench.Dataset, dataDir string) ([]bench.Row, error) {
	if err := ds.RequireFiles(s.name, s.file); err != nil {
		return nil, err
	}
	raw, err := ds.Open(dataDir, s.file)
	if err != nil {
		return nil, err
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("suites: %s %s: %w", s.name, s.file, err)
	}
	dec := json.NewDecoder(zr)
	tasks := map[string]evalplusTask{}
	var rows []bench.Row
	for {
		var t struct {
			ID string `json:"task_id"`
			evalplusTask
		}
		if err := dec.Decode(&t); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("suites: %s %s: %w", s.name, s.file, err)
		}
		if t.ID == "" || t.EntryPoint == "" || tasks[t.ID].EntryPoint != "" {
			return nil, fmt.Errorf("suites: %s %s has a task with no id or entry point, or one twice: %q", s.name, s.file, t.ID)
		}
		tasks[t.ID] = t.evalplusTask
		rows = append(rows, bench.Row{ID: t.ID, Text: t.Prompt, Label: t.EntryPoint})
	}
	s.tasks = tasks
	return rows, nil
}

// Prompt is the function stub as given, asking for the whole function back.
func (s *evalplus) Prompt(it bench.Item) (executor.Request, error) {
	stub, err := it.Text()
	if err != nil {
		return executor.Request{}, err
	}
	zero := 0.0
	return executor.Request{
		System: "Complete the Python function. Answer with the whole function, its imports and " +
			"signature included, in one ```python code block.",
		Prompt:      stub,
		Temperature: &zero,
		MaxTokens:   evalplusAnswerTokens,
	}, nil
}

// Grade passes an answer that passes every base and plus input. A run over its
// time limit is a fail marked Timeout, so a loaded machine shows in the report.
func (s *evalplus) Grade(ctx context.Context, it bench.Item, answer string) (bench.Grade, error) {
	return s.run(ctx, it, answer, true)
}

// BaseTests is the cheap check: the original HumanEval or MBPP inputs only.
func (s *evalplus) BaseTests(ctx context.Context, it bench.Item, answer string) (bench.Grade, error) {
	return s.run(ctx, it, answer, false)
}

func (s *evalplus) run(ctx context.Context, it bench.Item, answer string, plus bool) (bench.Grade, error) {
	t, ok := s.tasks[it.ID]
	if !ok {
		return bench.Grade{}, fmt.Errorf("suites: %s graded %s before it loaded", s.name, it.ID)
	}
	if runtime.GOOS == "windows" {
		return bench.Grade{}, errors.New("suites: EvalPlus grading needs the Unix time and memory limits")
	}
	// Looked up here: through the rlimit wrapper a missing python3 would read as a failed answer.
	if _, err := exec.LookPath("python3"); err != nil {
		return bench.Grade{}, fmt.Errorf("suites: %s grading needs python3: %w", s.name, err)
	}
	dir, err := os.MkdirTemp("", "hydra-evalplus-*")
	if err != nil {
		return bench.Grade{}, err
	}
	defer os.RemoveAll(dir)
	job, err := json.Marshal(map[string]any{
		"code": solution(answer, t), "reference": t.Prompt + t.Canonical, "entry_point": t.EntryPoint,
		"atol": t.Atol, "base": t.Base, "plus": t.Plus, "use_plus": plus,
	})
	if err != nil {
		return bench.Grade{}, err
	}
	runner, jobPath := filepath.Join(dir, "runner.py"), filepath.Join(dir, "job.json")
	if err := os.WriteFile(runner, evalplusRunner, 0o600); err != nil {
		return bench.Grade{}, err
	}
	if err := os.WriteFile(jobPath, job, 0o600); err != nil {
		return bench.Grade{}, err
	}
	o := oracle.CommandOracle{Args: []string{"python3", "-I", "-B", runner, jobPath}, Source: "bench:" + s.name,
		Dir: dir, MemMB: evalplusMemMB, CPUSeconds: evalplusCPUSecs}
	rctx, cancel := context.WithTimeout(ctx, s.deadline)
	defer cancel()
	v, err := o.Verify(rctx, "", trust.Task{})
	switch {
	case err != nil && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded):
		return bench.Grade{Timeout: true, Detail: "timeout: no verdict in " + s.deadline.String()}, nil
	case err != nil:
		return bench.Grade{}, err
	case v.Passed:
		return bench.Grade{Passed: true}, nil
	}
	return bench.Grade{Timeout: strings.HasPrefix(v.Detail, "timeout:"), Detail: v.Detail}, nil
}

// solution is the answer's first python code block, or the answer itself. A bare
// body is put back under the stub, so a model that answers with it is still judged.
func solution(answer string, t evalplusTask) string {
	code := answer
	if m := codeBlockRe.FindStringSubmatch(answer); m != nil {
		code = m[1]
	}
	if !strings.Contains(code, "def "+t.EntryPoint+"(") {
		code = t.Prompt + code
	}
	return code
}
