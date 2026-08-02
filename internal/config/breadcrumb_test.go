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
	testutil.WriteRegistry(t, dir, "routing: a", "models: b", "domains: c", "pricing: d")
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
	testutil.WriteRegistry(t, dir, "routing: a", "models: b", "domains: c", "pricing: d")
	t.Setenv("HYDRA_HOME", dir)

	before, err := config.Breadcrumb()
	if err != nil {
		t.Fatal(err)
	}

	testutil.WriteRegistry(t, dir, "routing: a-edited", "models: b", "domains: c", "pricing: d")
	after, err := config.Breadcrumb()
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Error("Breadcrumb should change when routing.yaml changes")
	}
}

// Plain concatenation would be ambiguous: moving content across a file boundary
// leaves the byte stream unchanged, so two materially different deployments
// would share a fingerprint — exactly what the breadcrumb exists to prevent.
func TestBreadcrumb_DistinguishesContentMovedAcrossFileBoundary(t *testing.T) {
	dirA := t.TempDir()
	testutil.WriteRegistry(t, dirA, "tier: 1\n", "model: x\n", "d: y\n", "p: z\n")
	t.Setenv("HYDRA_HOME", dirA)
	a, err := config.Breadcrumb()
	if err != nil {
		t.Fatal(err)
	}

	dirB := t.TempDir()
	testutil.WriteRegistry(t, dirB, "tier: 1\nmodel: x\n", "", "d: y\n", "p: z\n")
	t.Setenv("HYDRA_HOME", dirB)
	b, err := config.Breadcrumb()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("fingerprint collided across a file boundary: both %s", a[:16])
	}
}

// pricing.yaml drives cost-based routing, so an edit must change the identity.
func TestBreadcrumb_CoversPricing(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteRegistry(t, dir, "routing: a", "models: b", "domains: c", "tier1: 0.001")
	t.Setenv("HYDRA_HOME", dir)
	before, err := config.Breadcrumb()
	if err != nil {
		t.Fatal(err)
	}

	testutil.WriteRegistry(t, dir, "routing: a", "models: b", "domains: c", "tier1: 999.0")
	after, err := config.Breadcrumb()
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Error("a pricing.yaml edit must change the deployment fingerprint")
	}
}

func TestBreadcrumb_MissingRegistryErrors(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir()) // no registry/ dir at all
	if _, err := config.Breadcrumb(); err == nil {
		t.Error("Breadcrumb should error when registry files are unreadable")
	}
}
