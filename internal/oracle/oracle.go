// Package oracle turns deterministic verifiers — test runners, compilers,
// linters — into first-class evidence sources for the Trust Control Plane. A
// passing suite is far stronger evidence of correctness than any single model's
// opinion, so a calibrated oracle contributes a large log-likelihood ratio to
// the SPRT ensemble (SPEC §4: "verifiers as sources too").
package oracle

import (
	"context"
	"os"
	"os/exec"
	"strings"

	"github.com/ankit373/hydra/internal/trust"
)

// defaultWriteTemp materializes the candidate to a temp file for {file} oracles.
func defaultWriteTemp(content string) (path string, cleanup func(), err error) {
	f, err := os.CreateTemp("", "hydra-oracle-*.txt")
	if err != nil {
		return "", nil, err
	}
	name := f.Name()
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(name)
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", nil, err
	}
	return name, func() { os.Remove(name) }, nil
}

// Verdict is the outcome of a verification.
type Verdict struct {
	Passed bool
	Detail string // first line of output on failure, for diagnostics
}

// Oracle independently verifies a candidate answer for a task.
type Oracle interface {
	// Verify runs the check. It returns a Verdict; a non-nil error means the
	// oracle itself could not run (not that the candidate failed).
	Verify(ctx context.Context, candidate string, task trust.Task) (Verdict, error)
}

// CommandOracle runs an external command as the verifier. Exit code 0 is a pass;
// any non-zero exit is a fail. The candidate answer is written to a temp file
// and substituted for {file}; the raw answer is substituted for {answer}.
type CommandOracle struct {
	// Template is the command, e.g. "go test ./..." or "tsc --noEmit {file}".
	Template string
	// Source is the calibration key for this oracle, e.g. "verifier:go-test".
	Source string
	// writeTemp allows tests to stub file materialization; nil uses the real one.
	writeTemp func(content string) (path string, cleanup func(), err error)
}

// Verify runs the command oracle.
func (o *CommandOracle) Verify(ctx context.Context, candidate string, _ trust.Task) (Verdict, error) {
	parts, cleanup, err := o.buildArgs(candidate)
	if err != nil {
		return Verdict{}, err
	}
	if cleanup != nil {
		defer cleanup()
	}
	if len(parts) == 0 {
		return Verdict{Passed: true}, nil
	}
	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		if _, ok := runErr.(*exec.ExitError); ok {
			return Verdict{Passed: false, Detail: firstLine(string(out))}, nil
		}
		return Verdict{}, runErr // couldn't launch the verifier at all
	}
	return Verdict{Passed: true}, nil
}

// buildArgs splits the template, substituting {answer} inline and materializing
// {file} to a temp path. It mirrors editor.runValidatorCmd's safe splitting so
// paths with spaces are never fragmented.
func (o *CommandOracle) buildArgs(candidate string) (parts []string, cleanup func(), err error) {
	tmpl := o.Template
	if strings.Contains(tmpl, "{file}") {
		writer := o.writeTemp
		if writer == nil {
			writer = defaultWriteTemp
		}
		path, cl, werr := writer(candidate)
		if werr != nil {
			return nil, nil, werr
		}
		cleanup = cl
		idx := strings.Index(tmpl, "{file}")
		parts = append(strings.Fields(subAnswer(tmpl[:idx], candidate)), path)
		parts = append(parts, strings.Fields(subAnswer(tmpl[idx+len("{file}"):], candidate))...)
		return parts, cleanup, nil
	}
	return strings.Fields(subAnswer(tmpl, candidate)), nil, nil
}

func subAnswer(s, candidate string) string {
	return strings.ReplaceAll(s, "{answer}", candidate)
}

// LLR maps a verdict to the calibrated log-likelihood-ratio contribution of this
// oracle, exactly as any evidence source: a pass is "says correct", a fail is
// "says incorrect". A verifier calibrated to high sensitivity/specificity yields
// a large-magnitude contribution — dominating a single model's vote.
func LLR(cal *trust.Calibrator, source, domain string, v Verdict) float64 {
	return cal.LLR(source, domain, v.Passed)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
