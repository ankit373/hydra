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
	"github.com/ankit373/hydra/internal/util"
)

const (
	// outputCap bounds the verifier's combined stdout+stderr capture. This is
	// diagnostic output for the Detail field, not a primary answer, so it gets
	// the smaller "stderr-like" cap used elsewhere (cli.go, agy.go) rather than
	// the default unbounded-answer size.
	outputCap = 64 << 10

	// detailMaxLen bounds what firstLine ever returns, independent of the
	// Accumulator's cap — a single newline-free line can otherwise still be
	// outputCap bytes and flood the terminal.
	detailMaxLen = 4 << 10

	// maxArgvBytes is a conservative, cross-platform-safe guard against the OS
	// argv limit (ARG_MAX). Real limits vary (Linux ~2MB, macOS ~256KB-1MB
	// per historical defaults); this stays well under all of them.
	maxArgvBytes = 256 << 10
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
// verbatim for {file}; {answer} gets the same content with a trailing
// newline trimmed — a file-write artifact (echo, any editor), not part of
// the answer a verifier is comparing against.
type CommandOracle struct {
	// Args is the command argv, verbatim, e.g. []string{"sh", "-c", "exit 1"}.
	// Each element is substituted in place for {file}/{answer} and passed to
	// exec.Command as-is — never re-tokenized — so an element containing
	// whitespace survives intact. Preferred whenever real argv is available
	// (e.g. parsed CLI args); takes precedence over Template.
	Args []string
	// Template is the command as one string, e.g. "go test ./..." or
	// "tsc --noEmit {file}", split on whitespace. Only used when Args is
	// empty. Joining real argv into a Template and letting it re-split is
	// what corrupted any argument containing a space (#444) — a caller
	// holding argv must use Args instead.
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
	if err := checkArgvSize(parts, candidate); err != nil {
		return Verdict{}, err
	}
	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
	// Both streams share one bounded Accumulator, matching CombinedOutput's
	// interleaving — but capped, unlike the bytes.Buffer it replaces.
	out := util.NewAccumulator(outputCap)
	cmd.Stdout = out
	cmd.Stderr = out
	runErr := cmd.Run()
	if runErr != nil {
		if _, ok := runErr.(*exec.ExitError); ok {
			return Verdict{Passed: false, Detail: firstLine(out.String())}, nil
		}
		return Verdict{}, runErr // couldn't launch the verifier at all
	}
	return Verdict{Passed: true}, nil
}

// checkArgvSize rejects an argv too large to exec before the OS gets a chance
// to fail with a raw "fork/exec: argument list too long". Only {answer}
// substitution can grow argv this large; {file} always substitutes a short path.
func checkArgvSize(parts []string, candidate string) error {
	total := 0
	for _, p := range parts {
		total += len(p)
	}
	if total > maxArgvBytes {
		return fmt.Errorf("candidate too large to pass via {answer} (%d bytes) — use {file} instead", len(candidate))
	}
	return nil
}

// buildArgs builds the argv to execute. When Args holds real argv it is used
// verbatim (see buildArgsFromArgv, #444); otherwise it splits Template into
// argv, substituting {answer} inline and materializing {file} to a temp path.
// Both substitute as exactly one atomic argv element — never re-split by
// whitespace inside the substituted value, so a candidate answer containing
// whitespace or flag-like tokens cannot inject extra argv entries into
// whatever binary is named (CWE-88 argument injection).
func (o *CommandOracle) buildArgs(candidate string) (parts []string, cleanup func(), err error) {
	if len(o.Args) > 0 {
		return o.buildArgsFromArgv(candidate)
	}
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
	return splitTemplate(tmpl, answerFor(candidate), filePath), cleanup, nil
}

// answerFor is what fills an {answer} slot: the candidate with a trailing
// newline trimmed. {file} materialization uses the raw candidate untouched —
// a verifier reading the file itself (compiler, linter) must see the exact
// bytes the candidate was.
func answerFor(candidate string) string {
	return strings.TrimRight(candidate, "\r\n")
}

// buildArgsFromArgv substitutes {answer}/{file} inside each Args element in
// place, with no whitespace re-tokenization: an argv element containing
// spaces (e.g. a shell -c script) reaches exec.Command exactly as given.
// Joining such argv into a Template string and re-splitting it on whitespace
// is what silently corrupted arguments containing spaces (#444).
func (o *CommandOracle) buildArgsFromArgv(candidate string) (parts []string, cleanup func(), err error) {
	var filePath string
	for _, a := range o.Args {
		if !strings.Contains(a, "{file}") {
			continue
		}
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
		break
	}
	parts = make([]string, len(o.Args))
	for i, a := range o.Args {
		a = strings.ReplaceAll(a, "{answer}", answerFor(candidate))
		a = strings.ReplaceAll(a, "{file}", filePath)
		parts[i] = a
	}
	return parts, cleanup, nil
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
		s = s[:i]
	}
	if len(s) > detailMaxLen {
		return s[:detailMaxLen] + "...(truncated)"
	}
	return s
}
