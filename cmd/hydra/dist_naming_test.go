// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Five independent implementations reconstruct the release archive filename by
// hand — four installers plus goreleaser's own template — and nothing checked
// that they agree.
//
// That is exactly how #262 shipped: npm and pip both mapped arm64 → windows/arm64
// and cheerfully requested hydra_<ver>_windows_arm64.zip, an archive
// .goreleaser.yaml was configured never to build. Silent 404 at install time, on
// a platform nobody tested.
//
// These tests live in cmd/hydra alongside naming_test.go, the other repo-level
// guard, and read the real files rather than a copy of what they are believed to
// say. A sixth transcription of the rules in Go would be the same bug wearing a
// different hat.

func repoFile(t *testing.T, rel ...string) string {
	t.Helper()
	parts := append([]string{"..", ".."}, rel...)
	raw, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatalf("reading %s: %v", filepath.Join(rel...), err)
	}
	return string(raw)
}

type target struct{ os, arch string }

func (t target) String() string { return t.os + "/" + t.arch }

// builtTargets is the authoritative set: goos × goarch, minus ignore.
func builtTargets(t *testing.T) map[target]bool {
	t.Helper()

	var doc struct {
		Builds []struct {
			GOOS   []string `yaml:"goos"`
			GOARCH []string `yaml:"goarch"`
			Ignore []struct {
				GOOS   string `yaml:"goos"`
				GOARCH string `yaml:"goarch"`
			} `yaml:"ignore"`
		} `yaml:"builds"`
	}
	if err := yaml.Unmarshal([]byte(repoFile(t, ".goreleaser.yaml")), &doc); err != nil {
		t.Fatalf("parsing .goreleaser.yaml: %v", err)
	}
	if len(doc.Builds) == 0 {
		t.Fatal("no builds in .goreleaser.yaml — this guard has stopped guarding")
	}

	out := map[target]bool{}
	for _, b := range doc.Builds {
		for _, goos := range b.GOOS {
			for _, goarch := range b.GOARCH {
				out[target{goos, goarch}] = true
			}
		}
		for _, ig := range b.Ignore {
			delete(out, target{ig.GOOS, ig.GOARCH})
		}
	}
	if len(out) == 0 {
		t.Fatal("goreleaser builds nothing at all")
	}
	return out
}

// literalMapValues pulls the values out of a one-line literal map in source,
// e.g. `{ darwin: 'darwin', linux: 'linux', win32: 'windows' }`. Reading the
// installers' real maps is the point: a hand-copied list here would drift.
func literalMapValues(t *testing.T, src, anchor string) []string {
	t.Helper()
	i := strings.Index(src, anchor)
	if i < 0 {
		t.Fatalf("anchor %q not found — the installer was restructured and this guard "+
			"is no longer reading what it thinks it is", anchor)
	}
	rest := src[i:]
	open := strings.Index(rest, "{")
	closeAt := strings.Index(rest, "}")
	if open < 0 || closeAt < open {
		t.Fatalf("no literal map after anchor %q", anchor)
	}
	body := rest[open+1 : closeAt]

	valRe := regexp.MustCompile(`['"]([a-z0-9_]+)['"]`)
	var vals []string
	for _, pair := range strings.Split(body, ",") {
		// Take the value side of `key: value`.
		_, v, found := strings.Cut(pair, ":")
		if !found {
			continue
		}
		if m := valRe.FindStringSubmatch(v); m != nil {
			vals = append(vals, m[1])
		}
	}
	if len(vals) == 0 {
		t.Fatalf("parsed no values out of the map after %q", anchor)
	}
	return vals
}

// Every (os, arch) an installer can ask for must be an archive that exists.
// This is #262 stated as a property.
func TestInstallers_OnlyRequestTargetsThatAreBuilt(t *testing.T) {
	built := builtTargets(t)

	npm := repoFile(t, "npm", "scripts", "postinstall.js")
	pip := repoFile(t, "pip", "hyctl", "__init__.py")

	installers := []struct {
		name         string
		oses, arches []string
	}{
		{
			name:   "npm/scripts/postinstall.js",
			oses:   literalMapValues(t, npm, "const PLATFORM"),
			arches: literalMapValues(t, npm, "const ARCH"),
		},
		{
			name:   "pip/hyctl/__init__.py",
			oses:   literalMapValues(t, pip, "_OS ="),
			arches: literalMapValues(t, pip, "_ARCH ="),
		},
		{
			name:   "install.sh",
			oses:   shellCaseValues(t, repoFile(t, "install.sh"), `OS="`),
			arches: shellCaseValues(t, repoFile(t, "install.sh"), `ARCH="`),
		},
	}

	for _, inst := range installers {
		for _, goos := range dedupe(inst.oses) {
			for _, goarch := range dedupe(inst.arches) {
				tgt := target{goos, goarch}
				if !built[tgt] {
					t.Errorf("%s can resolve to %s, but .goreleaser.yaml does not build it — "+
						"that install 404s. This is #262.", inst.name, tgt)
				}
			}
		}
	}
}

// shellCaseValues pulls assigned values out of install.sh's case arms, e.g.
// `Darwin) OS="darwin" ;;`.
func shellCaseValues(t *testing.T, src, prefix string) []string {
	t.Helper()
	re := regexp.MustCompile(regexp.QuoteMeta(prefix) + `([a-z0-9_]+)"`)
	m := re.FindAllStringSubmatch(src, -1)
	if len(m) == 0 {
		t.Fatalf("no %s… assignments found in install.sh", prefix)
	}
	var out []string
	for _, g := range m {
		out = append(out, g[1])
	}
	return out
}

// The archive extension must match goreleaser's format_overrides, or the
// installer asks for a file that exists under a different name.
func TestInstallers_UseTheRightArchiveExtension(t *testing.T) {
	var doc struct {
		Archives []struct {
			Formats         []string `yaml:"formats"`
			FormatOverrides []struct {
				GOOS    string   `yaml:"goos"`
				Formats []string `yaml:"formats"`
			} `yaml:"format_overrides"`
		} `yaml:"archives"`
	}
	if err := yaml.Unmarshal([]byte(repoFile(t, ".goreleaser.yaml")), &doc); err != nil {
		t.Fatalf("parsing .goreleaser.yaml: %v", err)
	}
	if len(doc.Archives) == 0 {
		t.Fatal("no archives in .goreleaser.yaml")
	}
	a := doc.Archives[0]

	def := ""
	if len(a.Formats) > 0 {
		def = a.Formats[0]
	}
	perOS := map[string]string{}
	for _, o := range a.FormatOverrides {
		if len(o.Formats) > 0 {
			perOS[o.GOOS] = o.Formats[0]
		}
	}

	if def != "tar.gz" {
		t.Errorf("default archive format is %q; the installers assume tar.gz", def)
	}
	if perOS["windows"] != "zip" {
		t.Errorf("windows archive format is %q; npm and pip both assume zip", perOS["windows"])
	}

	// pip states it inline; npm derives it. Both must agree with the above.
	pip := repoFile(t, "pip", "hyctl", "__init__.py")
	if !strings.Contains(pip, `ext = "zip" if osname == "windows" else "tar.gz"`) {
		t.Error("pip's extension logic no longer reads `zip` on windows else `tar.gz` — " +
			"if it changed deliberately, update this contract with it")
	}
}

// install.sh only handles the OSes it can actually unpack. It downloads a
// .tar.gz unconditionally, so claiming Windows support would fetch a .zip and
// hand it to tar.
func TestInstallSh_DoesNotClaimWindows(t *testing.T) {
	src := repoFile(t, "install.sh")
	for _, o := range shellCaseValues(t, src, `OS="`) {
		if o == "windows" {
			t.Error("install.sh maps an OS to windows but always downloads .tar.gz — " +
				"it would hand a zip to tar. Windows needs install.ps1 (#264).")
		}
	}
	if !strings.Contains(src, "tar.gz") {
		t.Error("install.sh no longer references tar.gz; re-check this contract")
	}
}

// The README's platform table is a promise. It must not list a target that is
// not built — the badge claimed "macOS | Linux" while Windows binaries shipped,
// and the table was added in #262 precisely so the claim is checkable.
func TestReadme_PlatformTableMatchesTheBuildMatrix(t *testing.T) {
	built := builtTargets(t)
	readme := repoFile(t, "README.md")

	i := strings.Index(readme, "#### Platform support")
	if i < 0 {
		t.Fatal("README has no '#### Platform support' section — it was removed or renamed, " +
			"so nothing now states which platforms ship")
	}
	section := readme[i:]
	if j := strings.Index(section, "\n## "); j > 0 {
		section = section[:j]
	}

	for _, want := range []struct {
		label string
		t     target
	}{
		{"macOS", target{"darwin", "amd64"}},
		{"Linux", target{"linux", "amd64"}},
		{"Windows", target{"windows", "amd64"}},
	} {
		listed := strings.Contains(section, want.label)
		if listed && !built[want.t] {
			t.Errorf("README lists %s but %s is not built", want.label, want.t)
		}
		if !listed && built[want.t] {
			t.Errorf("%s is built but README's platform table does not mention %s", want.t, want.label)
		}
	}

	// ARM64 is the column #262 was about; if it is built it must be advertised.
	if built[target{"windows", "arm64"}] && !strings.Contains(section, "ARM64") {
		t.Error("windows/arm64 is built but the README table has no ARM64 column")
	}
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
