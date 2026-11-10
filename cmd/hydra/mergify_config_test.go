// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Mergify reads .mergify.yml from main only, so a mistake here surfaces at release
// time. These pin the two that cost most: a check name nothing reports, and trust
// granted on a branch name a fork can choose.

var checkCondition = regexp.MustCompile(`^-?check-[a-z-]+\s*=\s*(.+)$`)

// mergifyConfig decodes .mergify.yml with its anchors resolved.
func mergifyConfig(t *testing.T) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(repoFile(t, ".mergify.yml")), &doc); err != nil {
		t.Fatalf("parsing .mergify.yml: %v", err)
	}
	return doc
}

// conditionStrings flattens a condition tree (lists, and/or/not maps) to its leaves.
func conditionStrings(v any) []string {
	switch c := v.(type) {
	case string:
		return []string{strings.TrimSpace(c)}
	case []any:
		var out []string
		for _, e := range c {
			out = append(out, conditionStrings(e)...)
		}
		return out
	case map[string]any:
		var out []string
		for _, e := range c {
			out = append(out, conditionStrings(e)...)
		}
		return out
	}
	return nil
}

type workflowJob struct {
	Name     string         `yaml:"name"`
	Uses     string         `yaml:"uses"`
	With     map[string]any `yaml:"with"`
	Strategy struct {
		Matrix map[string]any `yaml:"matrix"`
	} `yaml:"strategy"`
}

type workflowDoc struct {
	On   map[string]any `yaml:"on"`
	Jobs map[string]workflowJob
	// Only workflow_call inputs matter here: a reusable workflow's matrix default.
	Inputs map[string]struct {
		Default any `yaml:"default"`
	} `yaml:"-"`
}

func loadWorkflow(t *testing.T, name string) workflowDoc {
	t.Helper()
	var w workflowDoc
	raw := workflowFile(t, name)
	if err := yaml.Unmarshal([]byte(raw), &w); err != nil {
		t.Fatalf("parsing %s: %v", name, err)
	}
	var call struct {
		On struct {
			WorkflowCall struct {
				Inputs map[string]struct {
					Default any `yaml:"default"`
				} `yaml:"inputs"`
			} `yaml:"workflow_call"`
		} `yaml:"on"`
	}
	_ = yaml.Unmarshal([]byte(raw), &call)
	w.Inputs = call.On.WorkflowCall.Inputs
	return w
}

var matrixRef = regexp.MustCompile(`\$\{\{\s*matrix\.([A-Za-z0-9_-]+)\s*\}\}`)

// combos expands a matrix into its rows; inputs resolves a fromJSON(inputs.x) include.
func combos(t *testing.T, matrix map[string]any, inputs map[string]any) []map[string]any {
	t.Helper()
	rows := []map[string]any{{}}
	for k, v := range matrix {
		vals, ok := v.([]any)
		if !ok || k == "include" || k == "exclude" {
			continue
		}
		var next []map[string]any
		for _, r := range rows {
			for _, val := range vals {
				n := map[string]any{k: val}
				for rk, rv := range r {
					n[rk] = rv
				}
				next = append(next, n)
			}
		}
		rows = next
	}
	switch inc := matrix["include"].(type) {
	case []any:
		rows = rows[:0]
		for _, e := range inc {
			rows = append(rows, e.(map[string]any))
		}
	case string:
		m := regexp.MustCompile(`fromJSON\(inputs\.([A-Za-z0-9_-]+)\)`).FindStringSubmatch(inc)
		if m == nil {
			t.Fatalf("matrix include %q is not fromJSON(inputs.x); teach this test to read it", inc)
		}
		var list []map[string]any
		if err := json.Unmarshal([]byte(inputs[m[1]].(string)), &list); err != nil {
			t.Fatalf("matrix input %s: %v", m[1], err)
		}
		rows = list
	}
	return rows
}

// expand names a matrix job as GitHub does: its expressions filled in, or with none,
// the row's values appended in parentheses.
func expand(t *testing.T, name string, row map[string]any) string {
	t.Helper()
	if matrixRef.MatchString(name) {
		return matrixRef.ReplaceAllStringFunc(name, func(s string) string {
			v, _ := row[matrixRef.FindStringSubmatch(s)[1]].(string)
			return v
		})
	}
	if len(row) == 0 {
		return name
	}
	if len(row) > 1 {
		t.Fatalf("job %q has a %d-key matrix; GitHub orders the suffix by declaration, which this test does not read", name, len(row))
	}
	for _, v := range row {
		return name + " (" + v.(string) + ")"
	}
	return name
}

// reportedChecks is every check name a pull_request run can report, from the workflows themselves.
func reportedChecks(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, file := range []string{"ci.yml", "cla.yml"} {
		w := loadWorkflow(t, file)
		if _, ok := w.On["pull_request"]; !ok {
			if _, ok := w.On["pull_request_target"]; !ok {
				t.Fatalf("%s no longer runs on pull requests; this test no longer checks what it thinks", file)
			}
		}
		for id, job := range w.Jobs {
			name := job.Name
			if name == "" {
				name = id
			}
			if !strings.HasPrefix(job.Uses, "./.github/workflows/") {
				for _, row := range combos(t, job.Strategy.Matrix, nil) {
					out[expand(t, name, row)] = true
				}
				continue
			}
			callee := loadWorkflow(t, strings.TrimPrefix(job.Uses, "./.github/workflows/"))
			inputs := map[string]any{}
			for k, in := range callee.Inputs {
				inputs[k] = in.Default
			}
			for k, v := range job.With {
				inputs[k] = v
			}
			for _, cj := range callee.Jobs {
				for _, row := range combos(t, cj.Strategy.Matrix, inputs) {
					out[name+" / "+expand(t, cj.Name, row)] = true
				}
			}
		}
	}
	return out
}

// A queue waiting on a check nothing reports never merges and says only "pending".
func TestMergify_EveryRequiredCheckIsReportedByAWorkflow(t *testing.T) {
	reported := reportedChecks(t)
	var missing []string
	seen := map[string]bool{}
	for _, c := range conditionStrings(mergifyConfig(t)) {
		m := checkCondition.FindStringSubmatch(c)
		if m == nil || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		if !reported[m[1]] {
			missing = append(missing, m[1])
		}
	}
	if len(seen) == 0 {
		t.Fatal("found no check conditions in .mergify.yml; this test no longer checks what it thinks")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		var have []string
		for k := range reported {
			have = append(have, k)
		}
		sort.Strings(have)
		t.Errorf(".mergify.yml requires checks no workflow reports, so the queue would wait on them for ever:\n  %s\nreported: %s",
			strings.Join(missing, "\n  "), strings.Join(have, ", "))
	}
}

func matchesOn(conds []string, attr string) bool {
	for _, c := range conds {
		if strings.HasPrefix(strings.TrimPrefix(c, "-"), attr) {
			return true
		}
	}
	return false
}

func pinsAuthor(conds []string) bool {
	for _, c := range conds {
		if strings.HasPrefix(c, "author=") {
			return true
		}
	}
	return false
}

// head and title belong to whoever opens the PR, so a rule that approves, queues or
// exempts on one must also name the author (the unpinned release-please rule, #1353).
func TestMergify_TrustOnABranchNameIsPinnedToAnAuthor(t *testing.T) {
	doc := mergifyConfig(t)
	check := func(kind, name string, conds []string) {
		if matchesOn(conds, "head") && !pinsAuthor(conds) {
			t.Errorf("%s %q matches on head without an author= condition; a fork can choose its branch name", kind, name)
		}
	}
	for _, r := range doc["queue_rules"].([]any) {
		rule := r.(map[string]any)
		check("queue rule", rule["name"].(string), conditionStrings(rule["queue_conditions"]))
	}
	for _, r := range doc["pull_request_rules"].([]any) {
		rule := r.(map[string]any)
		actions := rule["actions"].(map[string]any)
		_, review := actions["review"]
		_, queue := actions["queue"]
		if review || queue {
			check("pull_request_rule", rule["name"].(string), conditionStrings(rule["conditions"]))
		}
	}
	for _, r := range doc["merge_protections"].([]any) {
		rule := r.(map[string]any)
		ifs, _ := rule["if"].([]any)
		for _, e := range ifs {
			if s, ok := e.(string); ok && strings.HasPrefix(s, "-head") {
				t.Errorf("merge protection %q exempts %q by branch name alone; use not: with an author", rule["name"], s)
			}
			if m, ok := e.(map[string]any); ok && m["not"] != nil {
				check("merge protection exemption in", rule["name"].(string), conditionStrings(m["not"]))
			}
		}
	}
}
