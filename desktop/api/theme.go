// SPDX-License-Identifier: MIT

package api

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ankit373/hydra/internal/config"
)

// themeFile holds the viewer's light or dark choice, so the native window can
// paint that theme's background at launch; the page keeps its own copy in storage.
func themeFile() string { return filepath.Join(config.Dir(), "desktop-theme") }

// SaveTheme records "light" or "dark"; anything else clears the choice, and the
// window follows the OS again.
func (a *API) SaveTheme(mode string) error {
	if mode != "light" && mode != "dark" {
		if err := os.Remove(themeFile()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	// The app runs before `hyctl init` ever has, so the directory may not exist yet.
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(themeFile(), []byte(mode), 0o600)
}

// SavedTheme is the recorded choice, or "" when the viewer follows the OS.
func SavedTheme() string {
	b, err := os.ReadFile(themeFile())
	if err != nil {
		return ""
	}
	if s := strings.TrimSpace(string(b)); s == "light" || s == "dark" {
		return s
	}
	return ""
}
