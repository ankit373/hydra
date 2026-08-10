// SPDX-License-Identifier: MIT

// Package oracle turns deterministic verifiers — test runners, compilers,
// linters — into first-class evidence sources for the Trust Control Plane. A
// passing suite is far stronger evidence of correctness than any single model's
// opinion, so a calibrated oracle contributes a large log-likelihood ratio to
// the SPRT ensemble (SPEC §4: "verifiers as sources too").
package oracle

import (
	"context"
	"fmt"
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
		// An empty template is a misconfiguration, not a passing verdict.
		//
		// This returned Passed:true, which is the worst possible default here:
		// an oracle is a high-D evidence source, so LLR lets its verdict
		// outweigh several models' votes. A template that was blank, or lost in
		// config, therefore produced *confident false evidence* that the
		// candidate was correct — and did it silently, since nothing else in
		// the chain can tell an unconfigured oracle from a satisfied one.
		return Verdict{}, fmt.Errorf("oracle %s: empty command template", o.Source)
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

// buildArgs splits the template into argv, substituting {answer} inline and
// materializing {file} to a temp path. Both substitute as exactly one atomic
// argv element via splitTemplate — never re-split by whitespace inside the
// substituted value, so a candidate answer containing whitespace or flag-like
// tokens cannot inject extra argv entries into whatever binary the template
// names (CWE-88 argument injection).
func (o *CommandOracle) buildArgs(candidate string) (parts []string, cleanup func(), err error) {
	tmpl := o.Template
	var filePath string
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
		filePath = path
	}
	return splitTemplate(tmpl, candidate, filePath), cleanup, nil
}

// splitTemplate tokenizes tmpl into argv. Each {answer}/{file} placeholder
// substitutes as exactly one atomic argv element, in whatever order and
// however many times they appear; literal text around them is split on
// whitespace normally, mirroring editor.runValidatorCmd's handling of {file}.
func splitTemplate(tmpl, answer, file string) []string {
	var parts []string
	for {
		ai := strings.Index(tmpl, "{answer}")
		fi := strings.Index(tmpl, "{file}")
		var idx int
		var token, value string
		switch {
		case ai < 0 && fi < 0:
			return append(parts, strings.Fields(tmpl)...)
		case fi < 0 || (ai >= 0 && ai < fi):
			idx, token, value = ai, "{answer}", answer
		default:
			idx, token, value = fi, "{file}", file
		}
		parts = append(parts, strings.Fields(tmpl[:idx])...)
		parts = append(parts, value)
		tmpl = tmpl[idx+len(token):]
	}
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
