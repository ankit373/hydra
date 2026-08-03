// SPDX-License-Identifier: MIT

// External test package: internal/testutil imports config, so config's own
// in-package tests could not use the shared registry fixture without a cycle.
package config_test

import (
	"testing"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/registry"
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

// BreadcrumbFiles and the embedded set are maintained in different packages, so
// adding a file to one without the other would make Breadcrumb error on every
// installed binary again — silently, since callers stamp a blank fingerprint
// rather than failing.
func TestBreadcrumbFiles_AreAllEmbeddedInTheBinary(t *testing.T) {
	for _, name := range config.BreadcrumbFiles {
		if _, err := registry.Read("", name); err != nil {
			t.Errorf("%s is in BreadcrumbFiles but not embedded in the registry package: %v", name, err)
		}
	}
}

// This used to assert an error for a missing registry — which was every
// installed binary, so the fingerprint was absent from exactly the ledger,
// trust and cost logs it exists to stamp (#238). The registry is embedded now,
// so a machine with nothing on disk must still produce a stable fingerprint:
// that of the rules the binary actually shipped with.
func TestBreadcrumb_FingerprintsTheEmbeddedRulesWhenNothingIsOnDisk(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir()) // no registry/ dir at all

	first, err := config.Breadcrumb()
	if err != nil {
		t.Fatalf("Breadcrumb failed with no on-disk registry: %v", err)
	}
	if first == "" {
		t.Fatal("Breadcrumb returned an empty fingerprint")
	}

	second, err := config.Breadcrumb()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("embedded fingerprint is not stable: %s then %s", first, second)
	}
}
