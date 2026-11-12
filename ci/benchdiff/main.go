// SPDX-License-Identifier: MIT

// Command benchdiff routes a pinned prompt suite with no keys and no network,
// and diffs two runs into the routing-regression PR comment.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
)

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  benchdiff run -out head.json
      route suite.json in a hermetic home and write the decisions
  benchdiff compare -base base.json -head head.json [-out comment.md] [-fail-on-gate]
      render the diff; a missing base is reported, not fatal
      -fail-on-gate exits 1 when a gate is crossed (CI does not pass it yet)
`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = cmdRun(os.Args[2:])
	case "compare":
		err = cmdCompare(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "benchdiff: %v\n", err)
		os.Exit(1)
	}
}

func cmdRun(args []string) error {
	fl := flag.NewFlagSet("run", flag.ExitOnError)
	out := fl.String("out", "", "report path")
	_ = fl.Parse(args)
	if *out == "" {
		usage()
	}
	rep, err := runSuite(context.Background(), suiteJSON)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(*out, append(raw, '\n'), 0o644)
}

func cmdCompare(args []string) error {
	fl := flag.NewFlagSet("compare", flag.ExitOnError)
	basePath := fl.String("base", "", "base report")
	headPath := fl.String("head", "", "head report")
	out := fl.String("out", "", "markdown path (stdout when empty)")
	strict := fl.Bool("fail-on-gate", false, "exit 1 when a gate is crossed")
	_ = fl.Parse(args)
	if *basePath == "" || *headPath == "" {
		usage()
	}
	head, err := load(*headPath)
	if err != nil {
		return err
	}
	base, err := load(*basePath)
	note := ""
	if errors.Is(err, fs.ErrNotExist) {
		// The base cannot build this harness, e.g. the PR changed an API it calls.
		note = "Base did not produce a report, so every decision below is listed as new."
		base, err = Report{Schema: head.Schema, Suite: head.Suite}, nil
	}
	if err != nil {
		return err
	}
	d, err := compare(base, head)
	if err != nil {
		return err
	}
	if base.Suite != head.Suite && note == "" {
		note = fmt.Sprintf("Base ran suite `%s`, head `%s`: the PR changes the suite itself.", base.Suite, head.Suite)
	}
	md := render(d, head.Suite, note)
	if *out == "" {
		fmt.Print(md)
	} else if err := os.WriteFile(*out, []byte(md), 0o644); err != nil {
		return err
	}
	if *strict && len(d.Gate) > 0 {
		return fmt.Errorf("%d gate(s) crossed", len(d.Gate))
	}
	return nil
}

func load(path string) (Report, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Report{}, err
	}
	var r Report
	if err := json.Unmarshal(raw, &r); err != nil {
		return Report{}, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}
