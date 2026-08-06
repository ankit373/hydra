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
		{
			// install.ps1 is Windows-only by construction, so its OS is fixed
			// rather than parsed; only the arch switch can vary (#264).
			name:   "install.ps1",
			oses:   []string{"windows"},
			arches: psArchValues(t, repoFile(t, "install.ps1")),
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

// psArchValues pulls the architectures out of install.ps1's switch, e.g.
// `'ARM64' { $Arch = 'arm64' }`.
func psArchValues(t *testing.T, src string) []string {
	t.Helper()
	re := regexp.MustCompile(`\$Arch\s*=\s*'([a-z0-9_]+)'`)
	m := re.FindAllStringSubmatch(src, -1)
	if len(m) == 0 {
		t.Fatal("no $Arch assignments found in install.ps1 — restructured, and this " +
			"guard is no longer reading what it thinks it is")
	}
	var out []string
	for _, g := range m {
		out = append(out, g[1])
	}
	return out
}

// install.ps1 is the Windows counterpart to install.sh. It must verify
// checksums and fail closed on a checksums.txt that omits the archive — the
// #241 hole, and the one behaviour every other installer already shares.
func TestInstallPs1_VerifiesChecksumsAndFailsClosed(t *testing.T) {
	src := repoFile(t, "install.ps1")

	if !strings.Contains(src, "Get-FileHash") || !strings.Contains(src, "SHA256") {
		t.Error("install.ps1 does not compute a SHA256 of what it downloaded")
	}
	if !strings.Contains(src, "is not listed in checksums.txt") {
		t.Error("install.ps1 does not fail closed when checksums.txt omits the archive — " +
			"that is #241, and every other installer already refuses this case")
	}
	if !strings.Contains(src, "checksum mismatch") {
		t.Error("install.ps1 does not fail on a checksum mismatch")
	}
	// Per-user install: an installer that demands elevation for a CLI is a
	// worse default than one that asks the user to extend PATH.
	//
	// Matched on the environment variable rather than the prose "Program
	// Files", which appears in the script's own comment explaining why it is
	// not used — the first version of this check failed on that comment, which
	// is a fair reminder that a substring is not a usage.
	if regexp.MustCompile(`\$env:ProgramFiles|\$env:ProgramW6432`).MatchString(src) {
		t.Error("install.ps1 installs under Program Files, which needs elevation")
	}
	// PATH must be written at User scope only; Machine needs admin and changes
	// the environment for everyone on the box.
	if strings.Contains(src, "'Machine'") {
		t.Error("install.ps1 writes an environment variable at Machine scope")
	}
	if !strings.Contains(src, "LOCALAPPDATA") {
		t.Error("install.ps1 does not default to a per-user location")
	}
	// It must request the zip; Windows archives are not tar.gz.
	if !strings.Contains(src, ".zip") {
		t.Error("install.ps1 does not request a .zip")
	}
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

// ── The gaps the epic #254 revert audit found ────────────────────────────────
//
// #254's definition of done includes: "reverting any one of the eight v1.1.0
// fixes turns the suite red — the honest test of whether this suite is worth
// anything." Running that audit for real, three of the eight came back green.
// All three lived outside Go, which is why 90% statement coverage said nothing
// about them. These are the contracts that close that.

// #241 made npm and pip fail closed when checksums.txt downloads but does not
// list the archive. install.sh had always done this and install.ps1 was covered
// by a test — so the property was asserted for exactly one of the four
// installers, and reverting #241 left the suite green.
//
// The fatal call must be on the same line as the message. A test that only
// looked for the words would pass on a file that had moved them into a comment
// explaining why verification was skipped, which is the exact regression.
func TestInstallers_AllFailClosedOnAnUnlistedArchive(t *testing.T) {
	// Wording differs per installer on purpose — each speaks its own idiom, and
	// pinning one sentence across four languages would be a worse contract than
	// pinning the behaviour. What is common is: a fatal call, in the branch
	// where checksums.txt was fetched but has no entry for this archive.
	for _, c := range []struct {
		file  string
		fatal *regexp.Regexp // the fatal call, with a checksums.txt message in it
	}{
		{"install.sh", regexp.MustCompile(`\bdie\b.*checksums\.txt`)},
		{"install.ps1", regexp.MustCompile(`Stop-WithError.*checksums\.txt`)},
		{filepath.Join("npm", "scripts", "postinstall.js"), regexp.MustCompile(`\bfail\(.*checksums\.txt`)},
		{filepath.Join("pip", "hyctl", "__init__.py"), regexp.MustCompile(`_die\(.*checksums\.txt`)},
	} {
		t.Run(c.file, func(t *testing.T) {
			src := repoFile(t, strings.Split(c.file, string(filepath.Separator))...)
			if !strings.Contains(src, "checksums.txt") {
				t.Fatalf("%s never reads checksums.txt — it installs unverified", c.file)
			}
			if !c.fatal.MatchString(src) {
				t.Errorf("%s does not abort when checksums.txt omits the archive.\n"+
					"  That is #241: the file downloads, the entry is missing, and the install "+
					"continues unverified with no signal.\n"+
					"  Every other installer refuses this case; they must not disagree.", c.file)
			}
		})
	}
}

// #233: the desktop bundle claimed Wails' default 1.0.0 forever, because
// CFBundleShortVersionString is templated from wails.json's productVersion and
// nothing ever set it. macOS compares CFBundleVersion to decide what is newer,
// so every future release would still have looked like 1.0.0.
//
// The fix lives entirely in a workflow and two config files, so no amount of Go
// coverage could see it. Reverting it left the suite green.
func TestDesktopBundle_VersionIsStampedFromTheRelease(t *testing.T) {
	plist := repoFile(t, "desktop", "build", "darwin", "Info.plist")
	for _, key := range []string{"CFBundleVersion", "CFBundleShortVersionString"} {
		if !strings.Contains(plist, key) {
			t.Errorf("Info.plist has no %s — Finder cannot tell one build from another", key)
			continue
		}
		// Templated, not literal. A hardcoded version is the bug wearing a
		// different number: it goes stale on the very next release.
		if !regexp.MustCompile(key + `</key>\s*<string>\{\{`).MatchString(plist) {
			t.Errorf("Info.plist's %s is not templated from wails.json — "+
				"it will report the same version for every release (#233)", key)
		}
	}

	wf := repoFile(t, ".github", "workflows", "desktop-build.yml")
	if !strings.Contains(wf, "productVersion") {
		t.Error("desktop-build.yml never sets wails.json's info.productVersion, so the " +
			"templated Info.plist resolves to Wails' 1.0.0 default on every build (#233)")
	}
	// The other half of #233: goreleaser's checksums.txt is produced by a job
	// that never sees the desktop artifacts, so they shipped with no integrity
	// evidence at all — which matters more while the app is unsigned, not less.
	if !strings.Contains(wf, ".sha256") {
		t.Error("desktop-build.yml publishes no checksum beside the artifacts (#233)")
	}
}

// #243 deleted a Homebrew formula that no longer matched how the tap works —
// it named a binary that had been renamed, so anyone who found it got a broken
// install. Restoring the file is a silent regression: nothing imports it, so
// nothing fails.
func TestNoStaleHomebrewFormulaInTheRepo(t *testing.T) {
	// Two directories up from cmd/hydra is the repo root, the same convention
	// repoFile uses.
	if _, err := os.Stat(filepath.Join("..", "..", "Formula")); err == nil {
		t.Error("Formula/ is back. The tap is generated by goreleaser at release " +
			"time; a checked-in formula only competes with it and goes stale (#243). " +
			"If a hand-written formula is genuinely needed again, this test is the " +
			"place to say why.")
	}
}
