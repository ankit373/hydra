// SPDX-License-Identifier: MIT

package update

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// DownloadURL is a var so tests can serve release assets from httptest.
var DownloadURL = "https://github.com/ankit373/hydra/releases/download"

// lookGh finds the gh CLI; a var so a test can supply a fake one.
var lookGh = func() string { p, _ := exec.LookPath("gh"); return p }

const maxAssetBytes = 256 << 20

// Asset is one file on a release and the checksum file published beside it.
type Asset struct {
	Tag, Name, Sums string
	Attested        bool // CI publishes build provenance for this asset
}

// cliAsset is the hyctl archive for goos/goarch, checked against checksums.txt.
func cliAsset(tag, goos, goarch string) Asset {
	name := fmt.Sprintf("hydra_%s_%s_%s.tar.gz", strings.TrimPrefix(tag, "v"), goos, goarch)
	return Asset{Tag: tag, Name: name, Sums: "checksums.txt", Attested: true}
}

// AppAsset is the desktop archive for platform, which carries its own .sha256.
func AppAsset(tag, platform, ext string) Asset {
	name := fmt.Sprintf("hydra-desktop_%s_%s.%s", tag, platform, ext)
	return Asset{Tag: tag, Name: name, Sums: name + ".sha256"}
}

// InstallCLI installs hyctl from the latest release at dest.
func InstallCLI(ctx context.Context, dest string, log io.Writer) error {
	tag, err := Latest(ctx)
	if err != nil {
		return fmt.Errorf("could not find the latest release: %w", err)
	}
	a := cliAsset(tag, runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(log, "  Release : %s\n  Archive : %s\n", tag, a.Name)
	data, err := a.Download(ctx, log)
	if err != nil {
		return err
	}
	if err := InstallMember(data, "hyctl", dest); err != nil {
		return err
	}
	fmt.Fprintf(log, "  Installed: %s\n", dest)
	return nil
}

// Download fetches the asset and refuses it unless its sha256 matches the
// release's checksum file and, where gh can check it, its build provenance.
func (a Asset) Download(ctx context.Context, log io.Writer) ([]byte, error) {
	sums, err := fetch(ctx, a.Tag, a.Sums, 1<<20)
	if err != nil {
		return nil, fmt.Errorf("checksums unavailable, refusing to install: %w", err)
	}
	want, err := expectedSum(sums, a.Name)
	if err != nil {
		return nil, err
	}
	data, err := fetch(ctx, a.Tag, a.Name, maxAssetBytes)
	if err != nil {
		return nil, err
	}
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != want {
		return nil, fmt.Errorf("checksum mismatch for %s, refusing to install", a.Name)
	}
	fmt.Fprintf(log, "  Checksum: verified\n")
	if a.Attested {
		if err := checkProvenance(ctx, a.Name, data, log); err != nil {
			return nil, err
		}
	}
	return data, nil
}

func fetch(ctx context.Context, tag, name string, limit int64) ([]byte, error) {
	url := DownloadURL + "/" + tag + "/" + name
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d fetching %s", resp.StatusCode, url)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", name, limit)
	}
	return body, nil
}

// expectedSum reads the hash for name from sha256sum-format text.
func expectedSum(sums []byte, name string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && len(f[0]) == sha256.Size*2 {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("no checksum for %s, refusing to install", name)
}

// checkProvenance runs `gh attestation verify` when gh is installed and
// signed in; gh exit 4 means not signed in, so the check is skipped, not failed.
func checkProvenance(ctx context.Context, name string, data []byte, log io.Writer) error {
	gh := lookGh()
	if gh == "" {
		fmt.Fprintf(log, "  Provenance: not checked (gh not installed)\n")
		return nil
	}
	dir, err := os.MkdirTemp("", "hydra-attest-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, gh, "attestation", "verify", path, "--repo", "ankit373/hydra").CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		fmt.Fprintf(log, "  Provenance: verified\n")
		return nil
	case errors.As(err, &exit) && exit.ExitCode() == 4:
		fmt.Fprintf(log, "  Provenance: not checked (gh is not signed in)\n")
		return nil
	}
	return fmt.Errorf("provenance check failed for %s, refusing to install: %v\n%s", name, err, out)
}

// InstallMember extracts member from a .tar.gz and renames it over dest, so
// a running copy of dest keeps its old pages and dest is never half written.
func InstallMember(archive []byte, member, dest string) error {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("archive has no %s", member)
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeReg && filepath.Clean(h.Name) == member {
			return replaceFile(dest, tr)
		}
	}
}

func replaceFile(dest string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".hydra-upgrade-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, io.LimitReader(r, maxAssetBytes)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dest)
}

// DefaultBinDir is where a fresh hyctl goes: /usr/local/bin when writable,
// else ~/.local/bin, the same choice install.sh makes.
func DefaultBinDir() string {
	if f, err := os.CreateTemp("/usr/local/bin", ".hydra-probe-*"); err == nil {
		f.Close()
		os.Remove(f.Name())
		return "/usr/local/bin"
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "bin")
}
