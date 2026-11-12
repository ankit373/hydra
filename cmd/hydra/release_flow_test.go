// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Everything merges to main, and a release happens only when the maintainer
// approves release-please's PR (RELEASING.md). These pin the parts of that
// which fail silently: a release nobody approved, a docs pin release-please
// skips, and workflows still wired to develop.

// The edge build follows main, in one concurrency group: two runs in different
// groups race on deleting and recreating the same `edge` release (#337).
func TestEdgeWorkflow_BuildsFromMain(t *testing.T) {
	var wf struct {
		On struct {
			Push struct {
				Branches []string `yaml:"branches"`
			} `yaml:"push"`
		} `yaml:"on"`
		Concurrency struct {
			Group string `yaml:"group"`
		} `yaml:"concurrency"`
	}
	if err := yaml.Unmarshal([]byte(workflowFile(t, "edge.yml")), &wf); err != nil {
		t.Fatal(err)
	}
	if got := wf.On.Push.Branches; len(got) != 1 || got[0] != "main" {
		t.Errorf("edge.yml builds on pushes to %q, want only main", got)
	}
	if g := wf.Concurrency.Group; g == "" || strings.Contains(g, "${{") {
		t.Errorf("edge.yml concurrency group %q varies per run; two runs would race on the edge release", g)
	}
}

// Dependabot's default target is the default branch, main. A target-branch
// would send its PRs somewhere else.
func TestDependabot_HasNoTargetBranch(t *testing.T) {
	var cfg struct {
		Updates []map[string]any `yaml:"updates"`
	}
	if err := yaml.Unmarshal([]byte(repoFile(t, ".github", "dependabot.yml")), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Updates) == 0 {
		t.Fatal("dependabot.yml has no updates; this test no longer checks what it thinks")
	}
	for _, u := range cfg.Updates {
		if b, ok := u["target-branch"]; ok {
			t.Errorf("dependabot %v in %v targets %v; its PRs belong on main", u["package-ecosystem"], u["directory"], b)
		}
	}
}

// The issue sweep walks every PR in main's history, so run on a release it could
// close any open issue ever named in one. Issues close on merge now.
func TestReleaseWorkflows_DoNotSweepIssues(t *testing.T) {
	for _, name := range []string{"release-please.yml", "release.yml"} {
		for id, job := range loadWorkflow(t, name).Jobs {
			if strings.Contains(job.Uses, "close-shipped-issues") {
				t.Errorf("%s job %q runs the issue sweep on release", name, id)
			}
		}
	}
}

// With a release-type input the action builds its own config and ignores
// release-please-config.json, extra-files included, so the docs pins would
// never move and TestDocs_VersionIsNotBehindTheRelease would go red on every PR.
func TestReleasePleaseWorkflow_ReadsTheConfigFile(t *testing.T) {
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Uses string         `yaml:"uses"`
				With map[string]any `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(workflowFile(t, "release-please.yml")), &wf); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, job := range wf.Jobs {
		for _, step := range job.Steps {
			if !strings.HasPrefix(step.Uses, "googleapis/release-please-action@") {
				continue
			}
			found = true
			if _, ok := step.With["release-type"]; ok {
				t.Error("release-please.yml sets release-type, so the action ignores release-please-config.json")
			}
			if step.With["config-file"] != "release-please-config.json" {
				t.Errorf("release-please.yml reads config %v, want release-please-config.json", step.With["config-file"])
			}
		}
	}
	if !found {
		t.Fatal("release-please.yml has no release-please-action step; this test no longer checks what it thinks")
	}
}

type releasePleaseConfig struct {
	Packages map[string]struct {
		ExtraFiles []struct {
			Type string `json:"type"`
			Path string `json:"path"`
		} `json:"extra-files"`
	} `json:"packages"`
	raw map[string]any
}

func loadReleasePleaseConfig(t *testing.T) releasePleaseConfig {
	t.Helper()
	src := []byte(repoFile(t, "release-please-config.json"))
	var c releasePleaseConfig
	if err := json.Unmarshal(src, &c); err != nil {
		t.Fatalf("release-please-config.json: %v", err)
	}
	if err := json.Unmarshal(src, &c.raw); err != nil {
		t.Fatal(err)
	}
	return c
}

// release-as is sticky: every release proposes it until someone removes it.
// last-release-sha and bootstrap-sha move the changelog's start, and the two
// pre-major settings do nothing after 1.0 except mislead whoever reads them.
func TestReleasePleaseConfig_HasNoStickyOrPreMajorSettings(t *testing.T) {
	c := loadReleasePleaseConfig(t)
	bad := []string{"release-as", "last-release-sha", "bootstrap-sha", "bump-minor-pre-major", "bump-patch-for-minor-pre-major"}
	scopes := map[string]map[string]any{"top level": c.raw}
	if pkgs, ok := c.raw["packages"].(map[string]any); ok {
		for path, p := range pkgs {
			if m, ok := p.(map[string]any); ok {
				scopes["package "+path] = m
			}
		}
	}
	for where, m := range scopes {
		for _, k := range bad {
			if _, ok := m[k]; ok {
				t.Errorf("release-please-config.json sets %q at %s", k, where)
			}
		}
	}
}

// release-please's own patterns (updaters/generic.js).
var (
	semverRx       = regexp.MustCompile(`\d+\.\d+\.\d+(-[\w.]+)?(\+[-\w.]+)?`)
	inlineMarkerRx = regexp.MustCompile(`x-release-please-(major|minor|patch|version-date|version|date)`)
)

// bumpLikeReleasePlease applies release-please's generic updater: between
// x-release-please-start-version and x-release-please-end, and on any line
// marked x-release-please-version, it replaces the FIRST version on the line.
func bumpLikeReleasePlease(src, version string) string {
	lines := strings.Split(src, "\n")
	inBlock := false
	for i, line := range lines {
		switch {
		case inlineMarkerRx.MatchString(line):
			lines[i] = replaceFirst(line, version)
		case inBlock:
			lines[i] = replaceFirst(line, version)
			if strings.Contains(line, "x-release-please-end") {
				inBlock = false
			}
		case strings.Contains(line, "x-release-please-start-version"):
			inBlock = true
		}
	}
	return strings.Join(lines, "\n")
}

func replaceFirst(line, version string) string {
	loc := semverRx.FindStringIndex(line)
	if loc == nil {
		return line
	}
	return line[:loc[0]] + version + line[loc[1]:]
}

// The release PR must move every pinned version the docs tests read. The
// updater changes one version per line, so a line naming two (an asset URL's
// tag and filename) or a pin outside the markers would ship a 404.
func TestReleasePlease_BumpsEveryDocsVersionPin(t *testing.T) {
	const next = "99.88.77"
	pins := regexp.MustCompile(`(?:HYDRA_VERSION=v|releases/download/v|hydra-desktop_v|links pinned to v|"softwareVersion":\s*"|Release&nbsp;: <span class="g">v)(\d+\.\d+\.\d+)`)
	assets := regexp.MustCompile(`releases/download/v(\d+\.\d+\.\d+)/\n?hydra-desktop_v(\d+\.\d+\.\d+)_`)

	listed := map[string]bool{}
	for _, pkg := range loadReleasePleaseConfig(t).Packages {
		for _, f := range pkg.ExtraFiles {
			if f.Type != "generic" {
				t.Errorf("extra-file %s has type %q; this test replays only the generic updater", f.Path, f.Type)
			}
			listed[f.Path] = true
		}
	}
	for _, want := range []string{"README.md", "docs/app.html", "docs/index.html"} {
		if !listed[want] {
			t.Errorf("%s pins the release version but is not in release-please's extra-files", want)
		}
	}

	for path := range listed {
		src := repoFile(t, strings.Split(path, "/")...)
		bumped := bumpLikeReleasePlease(src, next)
		// Only pin lines may change: an unclosed block or a foreign version
		// inside one (a Wails or schema version) would be rewritten too.
		before, after := strings.Split(src, "\n"), strings.Split(bumped, "\n")
		for i := range before {
			if before[i] != after[i] && !pins.MatchString(before[i]) {
				t.Errorf("%s:%d: release-please would rewrite a line that is not a release pin: %q", path, i+1, before[i])
			}
		}
		ms := pins.FindAllStringSubmatch(bumped, -1)
		if len(ms) == 0 {
			t.Errorf("%s has no version pin; drop it from extra-files or fix this test", path)
		}
		for _, m := range ms {
			if m[1] != next {
				t.Errorf("%s: release-please would leave %q behind", path, m[0])
			}
		}
		for _, m := range assets.FindAllStringSubmatch(bumped, -1) {
			if m[1] != m[2] {
				t.Errorf("%s: after a bump an asset URL names tag v%s but file v%s", path, m[1], m[2])
			}
		}
	}
}

// release-please's PR is recognised partly by the files it touches, so every
// extra-file must be allowed there, or the bot's PR falls out of its own queue.
func TestMergify_ReleasePleaseFilesAllowTheExtraFiles(t *testing.T) {
	var anchor string
	for _, c := range conditionStrings(mergifyConfig(t)["shared"]) {
		if strings.HasPrefix(c, "-files~=^(?!(") {
			anchor = c
		}
	}
	if anchor == "" {
		t.Fatal("no release_please_files condition in .mergify.yml; this test no longer checks what it thinks")
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(anchor, "-files~=^(?!("), ")$)")
	allowed := regexp.MustCompile("^(" + inner + ")$")
	files := []string{"CHANGELOG.md", ".release-please-manifest.json"}
	for _, pkg := range loadReleasePleaseConfig(t).Packages {
		for _, f := range pkg.ExtraFiles {
			files = append(files, f.Path)
		}
	}
	for _, f := range files {
		if !allowed.MatchString(f) {
			t.Errorf("release-please's PR changes %s, which release_please_files does not allow", f)
		}
	}
}

// A release publishes for good, so only the maintainer's approval may queue it.
// Mergify must never approve it, and every queue into main other than
// release-please's own must refuse the bot outright.
func TestMergify_ReleasePRNeedsTheMaintainersApproval(t *testing.T) {
	doc := mergifyConfig(t)
	required := []string{"approved-reviews-by=ankit373", "label!=release:hold", "label!=release:blocked"}
	has := func(conds []string, want string) bool {
		for _, c := range conds {
			if c == want {
				return true
			}
		}
		return false
	}
	isReleasePlease := func(conds []string) bool { return has(conds, "head~=^release-please--") }

	sawQueue := false
	for _, r := range doc["queue_rules"].([]any) {
		rule := r.(map[string]any)
		conds := conditionStrings(rule["queue_conditions"])
		name := rule["name"].(string)
		switch {
		case isReleasePlease(conds):
			sawQueue = true
			for _, want := range required {
				if !has(conds, want) {
					t.Errorf("queue %q takes release-please's PR without %s", name, want)
				}
			}
		case has(conds, "base=main") && !pinsAuthor(conds) && !has(conds, "-author=github-actions[bot]"):
			t.Errorf("queue %q could take release-please's PR without the approval its own queue asks for", name)
		}
	}
	if !sawQueue {
		t.Fatal("no release-please queue in .mergify.yml; this test no longer checks what it thinks")
	}

	for _, r := range doc["pull_request_rules"].([]any) {
		rule := r.(map[string]any)
		conds := conditionStrings(rule["conditions"])
		if !isReleasePlease(conds) {
			continue
		}
		actions := rule["actions"].(map[string]any)
		if _, ok := actions["review"]; ok {
			t.Errorf("rule %q has Mergify review release-please's PR; only the maintainer approves a release", rule["name"])
		}
		if _, ok := actions["queue"]; ok {
			for _, want := range required {
				if !has(conds, want) {
					t.Errorf("rule %q queues release-please's PR without %s", rule["name"], want)
				}
			}
		}
	}
}
