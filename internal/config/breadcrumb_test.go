// SPDX-License-Identifier: MIT

// External test package: internal/testutil imports config, so config's own
// in-package tests could not use the shared registry fixture without a cycle.
package config_test

import (
	"testing"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/testutil"
)

func TestBreadcrumb_DeterministicForSameRegistry(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteRegistry(t, dir, "routing: a", "models: b", "domains: c")
	t.Setenv("HYDRA_HOME", dir)

	h1, err := config.Breadcrumb()
	if err != nil {
		t.Fatal(err)
	}
	h2, err := config.Breadcrumb()
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

// Guards against caching the fingerprint for the process lifetime: the
// long-running TUI must not keep serving a stale hash after a registry edit.
func TestBreadcrumb_ChangesWhenAnyRegistryFileChanges(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteRegistry(t, dir, "routing: a", "models: b", "domains: c")
	t.Setenv("HYDRA_HOME", dir)

	before, err := config.Breadcrumb()
	if err != nil {
		t.Fatal(err)
	}

	testutil.WriteRegistry(t, dir, "routing: a-edited", "models: b", "domains: c")
	after, err := config.Breadcrumb()
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Error("Breadcrumb should change when routing.yaml changes")
	}
}

func TestBreadcrumb_MissingRegistryErrors(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir()) // no registry/ dir at all
	if _, err := config.Breadcrumb(); err == nil {
		t.Error("Breadcrumb should error when registry files are unreadable")
	}
}
