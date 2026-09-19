// SPDX-License-Identifier: MIT

// Command hyverify runs a repository's own verifier against a candidate file
// and records the verdict as a labelled example. No router, no head selection
// and no Hydra config: a serving-side router never sees your test suite, and
// that is the ground truth this corpus is for (#967).
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/trust"
	"github.com/ankit373/hydra/internal/util"
	"github.com/ankit373/hydra/internal/verify"
)

// DefaultOut sits in the working directory, never in ~/.hydra: a tool that
// needs Hydra's home is not standalone.
const DefaultOut = ".hydra-evalset/examples.jsonl"

// maxOutput bounds what a verifier can print into one Detail line.
const maxOutput = 1 << 20

// Exit codes are the verdict: 0 verified and passed, 1 verified and failed,
// 2 no verdict at all. Folding the third into the second would file a
// refusal as a failing candidate.
const (
	exitPass      = 0
	exitFail      = 1
	exitNoVerdict = 2
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("hyverify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		candidate = fs.String("candidate", "", "file the verdict is about (required)")
		task      = fs.String("task", "", "what was asked; without it two tasks in one domain share an identity")
		domain    = fs.String("domain", "", "calibration domain (default: derived from the candidate)")
		enum      = fs.String("enum", "", "routing enum this example judges, when a router chose one")
		head      = fs.String("head", "", "model that produced the candidate")
		out       = fs.String("out", DefaultOut, "corpus to append to")
	)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "hyverify --candidate <file> [flags] [-- <command>...]\n\n"+
			"Runs the command that judges this repository against a candidate file and\n"+
			"records the verdict. With no command, the repository's own is resolved:\n"+
			"`go test ./...` in a Go module, else the configured validator for the\n"+
			"file's extension. Nothing configured is reported, never treated as a pass.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitNoVerdict
	}
	if strings.TrimSpace(*candidate) == "" {
		fs.Usage()
		return exitNoVerdict
	}

	abs, err := filepath.Abs(*candidate)
	if err != nil {
		fmt.Fprintf(stderr, "hyverify: %v\n", err)
		return exitNoVerdict
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		fmt.Fprintf(stderr, "hyverify: reading the candidate: %v\n", err)
		return exitNoVerdict
	}

	argv, label := resolve(fs.Args(), abs)
	if len(argv) == 0 {
		fmt.Fprintf(stderr, "hyverify: nothing is configured to judge %s.\n"+
			"  Name the command yourself: hyverify --candidate %s -- <command>\n",
			filepath.Base(abs), *candidate)
		return exitNoVerdict
	}
	if !reaches(abs, argv) {
		fmt.Fprintf(stderr, "hyverify: %s is neither named in the command nor inside the\n"+
			"directory it runs in, so the verdict would be about something else.\n",
			filepath.Base(abs))
		return exitNoVerdict
	}

	passed, detail := judge(argv)

	dom := *domain
	if dom == "" {
		dom = trust.DomainForFile(abs)
	}
	added, err := evalset.Add(*out, evalset.Example{
		TaskHash:  evalset.TaskHashFor(*task),
		Domain:    dom,
		Source:    "hyverify",
		Candidate: string(content),
		Passed:    passed,
		Detail:    detail,
		Enum:      *enum,
		Head:      *head,
	})
	if err != nil {
		fmt.Fprintf(stderr, "hyverify: recording the example: %v\n", err)
		return exitNoVerdict
	}

	verdict := "failed"
	if passed {
		verdict = "passed"
	}
	fmt.Fprintf(stdout, "%s %s (%s)\n", filepath.Base(abs), verdict, label)
	if added {
		fmt.Fprintf(stdout, "recorded in %s\n", *out)
	} else {
		fmt.Fprintf(stdout, "already recorded in %s\n", *out)
	}
	if strings.TrimSpace(*task) == "" {
		fmt.Fprintf(stdout, "no --task given, so this example shares an identity with "+
			"every other one in %s\n", dom)
	}
	if passed {
		return exitPass
	}
	return exitFail
}

// resolve prefers the command the caller named and otherwise asks
// internal/verify, the same resolution hyctl uses. A second one here would let
// the tool and Hydra disagree about what counts as a check, which is the one
// thing they must not do.
func resolve(explicit []string, candidate string) (argv []string, label string) {
	if len(explicit) > 0 {
		return explicit, strings.Join(explicit, " ")
	}
	return verify.Command(candidate)
}

// reaches is the rule #982 cost a merged PR to learn: a verdict is evidence
// about an answer only if the answer reached the thing the oracle inspects,
// through argv or through the filesystem it runs in. A dispatch satisfied
// neither, and its verdict described the repository instead.
func reaches(candidate string, argv []string) bool {
	for _, a := range argv {
		if p, err := filepath.Abs(a); err == nil && p == candidate {
			return true
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(wd, candidate)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// judge runs the verifier. Exit zero is the pass, as it is for every oracle in
// the tree; a command that cannot start is a failure to verify, not a failing
// candidate, so it says so in the detail rather than silently labelling one.
func judge(argv []string) (passed bool, detail string) {
	acc := util.NewAccumulator(maxOutput)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = acc, acc
	err := cmd.Run()
	out := firstLine(acc.String())
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			return false, "verifier did not run: " + err.Error()
		}
		if out == "" {
			out = "verifier exited non-zero"
		}
		return false, out
	}
	return true, out
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
