// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/ankit373/hydra/internal/build"
	"github.com/ankit373/hydra/internal/update"
	"github.com/ankit373/hydra/internal/util"
)

// UpdateStatus reports whether a newer Hydra release exists.
type UpdateStatus struct {
	Current   string `json:"current"`
	Latest    string `json:"latest,omitempty"`
	Available bool   `json:"available"`
}

// GetUpdateStatus checks for a newer release the same way hyctl's own startup
// banner does, internal/update's 24h-cached GitHub fetch and semver compare,
// but through update.CheckIgnoringTTY rather than update.Check: the desktop
// app has no stdout for the CLI's TTY gate to test, so Check would always
// silently answer "no update" here.
func (a *API) GetUpdateStatus() UpdateStatus {
	s := UpdateStatus{Current: build.Version}
	if latest := update.CheckIgnoringTTY(); latest != "" {
		s.Latest = latest
		s.Available = true
	}
	return s
}

// appExecutable is a var so a test can point TriggerUpgrade at a fake install.
var appExecutable = os.Executable

// UpgradeResult is what the upgrade printed and whether it finished.
type UpgradeResult struct {
	OK     bool   `json:"ok"`
	Output string `json:"output"`
}

// TriggerUpgrade replaces this app on disk with the latest release once its
// checksum matches. The running window keeps its old code until relaunch.
func (a *API) TriggerUpgrade() UpgradeResult {
	acc := util.NewAccumulator(0)
	ctx, cancel := context.WithTimeout(context.Background(), installRunTimeout)
	defer cancel()
	err := upgradeApp(ctx, acc)
	if err != nil {
		fmt.Fprintf(acc, "  x %v\n", err)
	}
	return UpgradeResult{OK: err == nil, Output: acc.String()}
}

func upgradeApp(ctx context.Context, log io.Writer) error {
	exe, err := appExecutable()
	if err != nil {
		return err
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return errors.New("on Windows, download the .zip from the releases page and unzip it")
	}
	tag, err := update.Latest(ctx)
	if err != nil {
		return fmt.Errorf("could not find the latest release: %w", err)
	}
	fmt.Fprintf(log, "  Release : %s\n", tag)
	if runtime.GOOS == "linux" {
		data, err := update.AppAsset(tag, "linux_"+runtime.GOARCH, "tar.gz").Download(ctx, log)
		if err != nil {
			return err
		}
		return update.InstallMember(data, "Hydra", exe)
	}
	data, err := update.AppAsset(tag, "darwin_universal", "zip").Download(ctx, log)
	if err != nil {
		return err
	}
	return replaceBundle(ctx, data, exe)
}

// replaceBundle unpacks the zip with ditto, which restores the symlinks and
// attributes the release kept, then swaps it for the bundle holding exe.
func replaceBundle(ctx context.Context, zip []byte, exe string) error {
	bundle := filepath.Dir(filepath.Dir(filepath.Dir(exe)))
	if filepath.Ext(bundle) != ".app" {
		return fmt.Errorf("%s is not inside an .app bundle", exe)
	}
	tmp, err := os.MkdirTemp(filepath.Dir(bundle), ".hydra-upgrade-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	zipPath := filepath.Join(tmp, "app.zip")
	if err := os.WriteFile(zipPath, zip, 0o600); err != nil {
		return err
	}
	out := filepath.Join(tmp, "out")
	if b, err := exec.CommandContext(ctx, "/usr/bin/ditto", "-x", "-k", zipPath, out).CombinedOutput(); err != nil {
		return fmt.Errorf("could not unpack the app: %v: %s", err, b)
	}
	fresh, err := filepath.Glob(filepath.Join(out, "*.app"))
	if err != nil || len(fresh) != 1 {
		return errors.New("the release zip does not hold exactly one .app bundle")
	}
	old := filepath.Join(tmp, "old.app")
	if err := os.Rename(bundle, old); err != nil {
		return err
	}
	if err := os.Rename(fresh[0], bundle); err != nil {
		_ = os.Rename(old, bundle)
		return err
	}
	return nil
}
