// SPDX-License-Identifier: MIT

//go:build unix

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// shellTools are what install.sh and install-app.sh call, minus uname and
// curl (faked) and the sha tools (added only when a case wants them).
var shellTools = []string{"mktemp", "rm", "grep", "sed", "awk", "tar", "chmod", "mkdir", "mv", "cat", "cp", "find", "basename", "ln", "head"}

// scriptRun is one run of an installer against a local fake release.
type scriptRun struct {
	script  string            // install.sh or install-app.sh
	files   map[string][]byte // release assets by name
	noSha   bool              // hide sha256sum and shasum
	env     []string
	home    string // set by run
	release string // set by run
}

func tgz(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(body)
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func sumLine(name string, body []byte) []byte {
	return []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(body), name))
}

// run executes the script with PATH limited to fakes and real basic tools.
func (r *scriptRun) run(t *testing.T) (string, error) {
	t.Helper()
	r.home, r.release = t.TempDir(), t.TempDir()
	for name, body := range r.files {
		if err := os.WriteFile(filepath.Join(r.release, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bin := t.TempDir()
	tools := shellTools
	if !r.noSha {
		tools = append(tools, "sha256sum", "shasum")
	}
	for _, tool := range tools {
		if p, err := exec.LookPath(tool); err == nil {
			_ = os.Symlink(p, filepath.Join(bin, tool))
		}
	}
	fakes := map[string]string{
		"uname": "#!/bin/sh\ncase \"$1\" in -s) echo Linux ;; -m) echo x86_64 ;; esac\n",
		// curl -fsSL [-o out] URL: serve the URL's last path element from the release dir.
		"curl": "#!/bin/sh\nout=\nwhile [ $# -gt 1 ]; do [ \"$1\" = -o ] && { out=$2; shift; }; shift; done\n" +
			"f=\"" + r.release + "/${1##*/}\"\n[ -f \"$f\" ] || exit 22\n" +
			"if [ -n \"$out\" ]; then cat \"$f\" > \"$out\"; else cat \"$f\"; fi\n",
	}
	for name, body := range fakes {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("/bin/sh", filepath.Join("..", "..", r.script))
	cmd.Env = append([]string{"PATH=" + bin, "HOME=" + r.home, "HYDRA_VERSION=v9.9.9",
		"HYDRA_BIN=" + filepath.Join(r.home, "bin"), "HYDRA_APP_DIR=" + filepath.Join(r.home, "app")}, r.env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// installerCase builds the release for one script; installed is the file a
// successful install writes.
func installerCase(t *testing.T, script string, withSums bool) (*scriptRun, func(home string) string) {
	t.Helper()
	if script == "install.sh" {
		name := "hydra_9.9.9_linux_amd64.tar.gz"
		archive := tgz(t, "hyctl", []byte("new"))
		r := &scriptRun{script: script, files: map[string][]byte{name: archive}}
		if withSums {
			r.files["checksums.txt"] = sumLine(name, archive)
		}
		return r, func(home string) string { return filepath.Join(home, "bin", "hyctl") }
	}
	name := "hydra-desktop_v9.9.9_linux_amd64.tar.gz"
	archive := tgz(t, "Hydra", []byte("new"))
	r := &scriptRun{script: script, files: map[string][]byte{name: archive}}
	if withSums {
		r.files[name+".sha256"] = sumLine(name, archive)
	}
	return r, func(home string) string { return filepath.Join(home, "app", "hydra-desktop") }
}

// Both POSIX installers must refuse to install when the checksum cannot be
// checked, and install only on an explicit HYDRA_INSECURE=1.
func TestShellInstallers_RefuseWhenTheChecksumCannotBeChecked(t *testing.T) {
	for _, script := range []string{"install.sh", "install-app.sh"} {
		cases := []struct {
			name      string
			withSums  bool
			noSha     bool
			env       []string
			installed bool
		}{
			{"verified", true, false, nil, true},
			{"no checksum file", false, false, nil, false},
			{"no sha tool", true, true, nil, false},
			{"opt out", false, true, []string{"HYDRA_INSECURE=1"}, true},
		}
		for _, c := range cases {
			t.Run(script+"/"+c.name, func(t *testing.T) {
				r, installed := installerCase(t, script, c.withSums)
				r.noSha, r.env = c.noSha, c.env
				out, err := r.run(t)
				_, statErr := os.Stat(installed(r.home))
				if got := err == nil && statErr == nil; got != c.installed {
					t.Errorf("installed = %v, want %v (err %v)\n%s", got, c.installed, err, out)
				}
				if !c.installed && !strings.Contains(out, "refusing to install") {
					t.Errorf("no reason given:\n%s", out)
				}
			})
		}
	}
}
