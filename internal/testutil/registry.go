// SPDX-License-Identifier: MIT

// Package testutil holds test fixtures shared across packages. Go test helpers
// cannot be imported from another package's _test.go files, so anything more
// than one package needs lives here.
package testutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ankit373/hydra/internal/config"
)

// WriteRegistry writes a minimal registry/ tree under dir with the given file
// contents, so config.Breadcrumb() has something to fingerprint. Point
// HYDRA_HOME at dir (via t.Setenv) to make it the resolved ScriptHome.
func WriteRegistry(t *testing.T, dir string, contents ...string) {
	t.Helper()
	if len(contents) != 0 && len(contents) != len(config.BreadcrumbFiles) {
		t.Fatalf("WriteRegistry: got %d contents, want 0 or %d", len(contents), len(config.BreadcrumbFiles))
	}
	regDir := filepath.Join(dir, "registry")
	if err := os.MkdirAll(regDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i, name := range config.BreadcrumbFiles {
		body := "x"
		if len(contents) != 0 {
			body = contents[i]
		}
		if err := os.WriteFile(filepath.Join(regDir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
