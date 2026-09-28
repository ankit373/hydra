// SPDX-License-Identifier: MIT

// Package vet reviews a diff. open-code-review resolves which files are worth
// reviewing and by what rules; Hydra's router decides which head reads each.
package vet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/ankit373/hydra/internal/util"
)

// ruleBinary is the open-code-review CLI. Only its delegate mode is used, which
// resolves files and rules with no model, no key and no spend.
const ruleBinary = "ocr"

// supportedSchema is the delegate contract this parser was written against.
// A later one may move fields, and a silent mis-parse reads as "no findings",
// which is the one wrong answer a reviewer must never give.
const supportedSchema = "1"

// ErrNoRuleSource separates "this machine has no reviewer" from "the review
// failed", so a caller prints an install line rather than a decode error.
var ErrNoRuleSource = errors.New("open-code-review is not installed")

// File is one file in the diff. Reason is empty on a reviewable file and names
// the filter that dropped an excluded one.
type File struct {
	Path       string `json:"path"`
	Status     string `json:"status"`
	Insertions int    `json:"insertions"`
	Deletions  int    `json:"deletions"`
	Reason     string `json:"exclude_reason,omitempty"`
}

// Group is one rule pack and the files it governs.
type Group struct {
	ID      int      `json:"group_id"`
	Source  string   `json:"source"`
	Pattern string   `json:"pattern"`
	Files   []string `json:"files"`
	Rule    string   `json:"rule"`
}

// Spec is the resolved review: which diff to read, and by what rules.
type Spec struct {
	SchemaVersion string `json:"schema_version"`
	Mode          string `json:"mode"` // workspace | commit | range
	Repository    string `json:"repository"`
	From          string `json:"from,omitempty"`
	To            string `json:"to,omitempty"`
	MergeBase     string `json:"merge_base,omitempty"`
	Commit        string `json:"commit,omitempty"`
	// Background is the commit message in commit mode, which is the author's
	// own statement of intent and the only context a diff cannot carry.
	Background string `json:"background,omitempty"`

	Reviewable []File `json:"reviewable_files"`
	Excluded   []File `json:"excluded_files"`

	Groups []Group `json:"groups,omitempty"`
}

// Options selects the diff. All three empty means the workspace.
type Options struct {
	Repo    string
	From    string
	To      string
	Commit  string
	Exclude string
}

// RuleFor returns the rule pack governing path, and whether one was resolved.
// A file with no rule is not reviewed: an unruled review is a head answering
// from whatever it happens to believe, which is what the packs exist to replace.
func (s *Spec) RuleFor(path string) (Group, bool) {
	for _, g := range s.Groups {
		for _, f := range g.Files {
			if f == path {
				return g, true
			}
		}
	}
	return Group{}, false
}

// Resolve asks the rule source what to review and how.
func Resolve(ctx context.Context, opts Options) (*Spec, error) {
	if _, err := exec.LookPath(ruleBinary); err != nil {
		return nil, ErrNoRuleSource
	}

	var spec Spec
	if err := delegate(ctx, opts, []string{"preview"}, &spec); err != nil {
		return nil, err
	}
	if v := strings.TrimSpace(spec.SchemaVersion); v != "" && v != supportedSchema {
		return nil, fmt.Errorf("%s delegate speaks schema %s, this build reads %s", ruleBinary, v, supportedSchema)
	}
	if len(spec.Reviewable) == 0 {
		return &spec, nil
	}

	paths := make([]string, 0, len(spec.Reviewable))
	for _, f := range spec.Reviewable {
		paths = append(paths, f.Path)
	}
	var rules struct {
		SchemaVersion string  `json:"schema_version"`
		Groups        []Group `json:"groups"`
	}
	if err := delegate(ctx, opts, append([]string{"rule"}, paths...), &rules); err != nil {
		return nil, err
	}
	spec.Groups = rules.Groups
	return &spec, nil
}

// args renders the diff selector. Commit wins over a range because the two are
// different questions and the CLI refuses both at once.
func (o Options) args() []string {
	var a []string
	if o.Repo != "" {
		a = append(a, "--repo", o.Repo)
	}
	switch {
	case o.Commit != "":
		a = append(a, "--commit", o.Commit)
	default:
		if o.From != "" {
			a = append(a, "--from", o.From)
		}
		if o.To != "" {
			a = append(a, "--to", o.To)
		}
	}
	if o.Exclude != "" {
		a = append(a, "--exclude", o.Exclude)
	}
	return a
}

func delegate(ctx context.Context, opts Options, sub []string, out any) error {
	args := append([]string{"delegate"}, sub...)
	args = append(args, opts.args()...)
	args = append(args, "--format", "json", "--color", "never")

	cmd := exec.CommandContext(ctx, ruleBinary, args...)
	stdout := util.NewAccumulator(util.DefaultMaxBytes)
	stderr := util.NewAccumulator(64 << 10)
	cmd.Stdout, cmd.Stderr = stdout, stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s delegate %s: %w: %s", ruleBinary, sub[0], err, strings.TrimSpace(stderr.String()))
	}
	// A truncated document parses as far as it parses, so a capped read would
	// drop files rather than fail, and a dropped file is one nobody reviewed.
	if stdout.Truncated() {
		return fmt.Errorf("%s delegate %s: output exceeded %d bytes", ruleBinary, sub[0], util.DefaultMaxBytes)
	}
	if err := json.Unmarshal([]byte(stdout.String()), out); err != nil {
		return fmt.Errorf("%s delegate %s: decode: %w", ruleBinary, sub[0], err)
	}
	return nil
}
