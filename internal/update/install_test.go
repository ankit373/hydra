// SPDX-License-Identifier: MIT

package update

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

// fakeRelease serves tag with files and points ReleaseURL and DownloadURL at
// it. gh is hidden so provenance is skipped unless a test supplies one.
func fakeRelease(t *testing.T, tag string, files map[string][]byte) {
	t.Helper()
	api, dl := testutil.FakeRelease(t, tag, files)
	origAPI, origDL, origGh := ReleaseURL, DownloadURL, lookGh
	ReleaseURL, DownloadURL, lookGh = api, dl, func() string { return "" }
	t.Cleanup(func() { ReleaseURL, DownloadURL, lookGh = origAPI, origDL, origGh })
}

// cliRelease is a v9.9.9 release whose hyctl archive holds body.
func cliRelease(t *testing.T, body []byte, sums func(name string, archive []byte) string) {
	t.Helper()
	a := cliAsset("v9.9.9", runtime.GOOS, runtime.GOARCH)
	archive := testutil.TarGz(t, "hyctl", body)
	files := map[string][]byte{a.Name: archive}
	if sums != nil {
		files["checksums.txt"] = []byte(sums(a.Name, archive))
	}
	fakeRelease(t, "v9.9.9", files)
}

func installOver(t *testing.T) (dest, log string, err error) {
	t.Helper()
	dest = filepath.Join(t.TempDir(), "hyctl")
	if err := os.WriteFile(dest, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	err = InstallCLI(context.Background(), dest, &buf)
	return dest, buf.String(), err
}

func TestInstallCLI_ReplacesTheBinaryWhenTheChecksumMatches(t *testing.T) {
	cliRelease(t, []byte("new"), func(name string, a []byte) string {
		return "deadbeef  other.tar.gz\n" + testutil.SumLine(name, a)
	})
	dest, log, err := installOver(t)
	if err != nil {
		t.Fatalf("InstallCLI: %v\n%s", err, log)
	}
	if got, _ := os.ReadFile(dest); string(got) != "new" {
		t.Errorf("dest holds %q, want the new binary", got)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(dest); fi.Mode().Perm()&0o100 == 0 {
			t.Errorf("mode %v is not executable", fi.Mode())
		}
	}
	if !strings.Contains(log, "Checksum: verified") {
		t.Errorf("log does not say the checksum was checked:\n%s", log)
	}
}

// Every way the checksum cannot be confirmed must leave the old binary alone.
func TestInstallCLI_RefusesWhenTheChecksumCannotBeConfirmed(t *testing.T) {
	cases := map[string]func(string, []byte) string{
		"no checksums.txt": nil,
		"asset not listed": func(string, []byte) string { return testutil.SumLine("other.tar.gz", []byte("x")) },
		"wrong hash":       func(name string, _ []byte) string { return testutil.SumLine(name, []byte("tampered")) },
		"malformed line":   func(name string, _ []byte) string { return "not-a-hash  " + name + "\n" },
	}
	for name, sums := range cases {
		t.Run(name, func(t *testing.T) {
			cliRelease(t, []byte("new"), sums)
			dest, _, err := installOver(t)
			if err == nil {
				t.Fatal("InstallCLI succeeded without a confirmed checksum")
			}
			if got, _ := os.ReadFile(dest); string(got) != "old" {
				t.Errorf("dest was changed to %q", got)
			}
		})
	}
}

func TestInstallMember_MissingMemberLeavesDestAlone(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "hyctl")
	if err := os.WriteFile(dest, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := InstallMember(testutil.TarGz(t, "hyverify", []byte("x")), "hyctl", dest); err == nil {
		t.Fatal("InstallMember succeeded with no hyctl in the archive")
	}
	if got, _ := os.ReadFile(dest); string(got) != "old" {
		t.Errorf("dest was changed to %q", got)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(dest), ".hydra-upgrade-*")); len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

func TestAssetNames_MatchTheReleaseLayout(t *testing.T) {
	if got := cliAsset("v1.8.0", "darwin", "arm64").Name; got != "hydra_1.8.0_darwin_arm64.tar.gz" {
		t.Errorf("cli asset = %q", got)
	}
	a := AppAsset("v1.8.0", "darwin_universal", "zip")
	if a.Name != "hydra-desktop_v1.8.0_darwin_universal.zip" || a.Sums != a.Name+".sha256" {
		t.Errorf("app asset = %+v", a)
	}
}

// The tag becomes part of the download path, so anything else is refused.
func TestLatest_RefusesATagThatIsNotAVersion(t *testing.T) {
	for _, tag := range []string{"", "../../x/v1.0.0", "v1.0.0/../x", "main"} {
		fakeRelease(t, tag, nil)
		if got, err := Latest(context.Background()); err == nil {
			t.Errorf("Latest() = %q for tag %q, want an error", got, tag)
		}
	}
}

// An oversized checksum file is refused rather than read without bound.
func TestInstallCLI_RefusesAnOversizedChecksumFile(t *testing.T) {
	cliRelease(t, []byte("new"), func(name string, a []byte) string {
		return testutil.SumLine(name, a) + strings.Repeat("#", 1<<20)
	})
	if _, _, err := installOver(t); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("err = %v, want a size refusal", err)
	}
}

func TestDefaultBinDir_IsInstallShsChoice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if got := DefaultBinDir(); got != "/usr/local/bin" && got != filepath.Join(home, ".local", "bin") {
		t.Errorf("DefaultBinDir() = %q", got)
	}
}

func TestInstallCLI_UnreachableReleaseFails(t *testing.T) {
	cliRelease(t, []byte("new"), nil)
	ReleaseURL, DownloadURL = "http://127.0.0.1:0/latest", "http://127.0.0.1:0/dl"
	if _, _, err := installOver(t); err == nil || !strings.Contains(err.Error(), "latest release") {
		t.Errorf("err = %v, want a latest-release failure", err)
	}
	if _, err := fetch(context.Background(), "v1.0.0", "x", 1); err == nil {
		t.Error("fetch from an unreachable host succeeded")
	}
}
