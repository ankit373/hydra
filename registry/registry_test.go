// SPDX-License-Identifier: MIT

package registry

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// An embedded file that fails to parse is worse than a missing one: the binary
// would ship rules nothing can read, and every consumer would silently fall
// back to its own defaults, which is the failure #238 was about.
func TestEmbedded_EveryFileParsesAndIsNotEmpty(t *testing.T) {
	names := Names()
	if len(names) == 0 {
		t.Fatal("no files embedded, go:embed matched nothing")
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			raw, err := Read("", name)
			if err != nil {
				t.Fatalf("embedded %s unreadable: %v", name, err)
			}
			if len(raw) == 0 {
				t.Fatalf("embedded %s is empty", name)
			}
			var doc any
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("embedded %s is not valid YAML: %v", name, err)
			}
			if doc == nil {
				t.Fatalf("embedded %s parses to nothing", name)
			}
		})
	}
}

// The registry stays editable YAML precisely so operators can retune routing
// without a rebuild. If the override silently stopped winning, edits would
// appear to do nothing and there would be no error to notice.
func TestRead_OnDiskCopyOverridesTheEmbeddedOne(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "registry")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := "tiers:\n  1:\n    in: 42.0\n"
	if err := os.WriteFile(filepath.Join(dir, "pricing.yaml"), []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Read(home, "pricing.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("on-disk copy ignored:\n got %q\nwant %q", got, want)
	}

	// A file the operator has not overridden still comes from the binary.
	other, err := Read(home, "models.yaml")
	if err != nil {
		t.Fatalf("un-overridden file unreadable: %v", err)
	}
	embeddedModels, _ := Read("", "models.yaml")
	if string(other) != string(embeddedModels) {
		t.Error("un-overridden file did not come from the embedded copy")
	}
}

// A home with no registry/ directory is the normal case for an installed
// binary, and must not be an error.
func TestRead_FallsBackToEmbeddedWhenHomeHasNoRegistry(t *testing.T) {
	raw, err := Read(t.TempDir(), "routing.yaml")
	if err != nil {
		t.Fatalf("Read failed with no on-disk registry: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("Read returned nothing")
	}
}

func TestRead_UnknownFileIsAnError(t *testing.T) {
	if _, err := Read("", "nope.yaml"); err == nil {
		t.Error("Read should fail for a file that is neither on disk nor embedded")
	}
}
