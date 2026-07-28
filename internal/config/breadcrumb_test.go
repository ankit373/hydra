// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeRegistry(t *testing.T, dir string, routing, models, domains string) {
	t.Helper()
	regDir := filepath.Join(dir, "registry")
	if err := os.MkdirAll(regDir, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"routing.yaml": routing, "models.yaml": models, "domains.yaml": domains}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(regDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBreadcrumb_DeterministicForSameRegistry(t *testing.T) {
	dir := t.TempDir()
	writeRegistry(t, dir, "routing: a", "models: b", "domains: c")
	t.Setenv("HYDRA_HOME", dir)

	h1, err := Breadcrumb()
	if err != nil {
		t.Fatal(err)
	}
	h2, err := Breadcrumb()
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Errorf("Breadcrumb should be deterministic for an unchanged registry: %s != %s", h1, h2)
	}
	if len(h1) != 64 { // SHA256 hex
		t.Errorf("Breadcrumb length = %d, want 64 (sha256 hex)", len(h1))
	}
}

func TestBreadcrumb_ChangesWhenAnyRegistryFileChanges(t *testing.T) {
	dir := t.TempDir()
	writeRegistry(t, dir, "routing: a", "models: b", "domains: c")
	t.Setenv("HYDRA_HOME", dir)

	before, err := Breadcrumb()
	if err != nil {
		t.Fatal(err)
	}

	writeRegistry(t, dir, "routing: a-edited", "models: b", "domains: c")
	after, err := Breadcrumb()
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Error("Breadcrumb should change when routing.yaml changes")
	}
}

func TestBreadcrumb_MissingRegistryErrors(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir()) // no registry/ dir at all
	if _, err := Breadcrumb(); err == nil {
		t.Error("Breadcrumb should error when registry files are unreadable")
	}
}
