// SPDX-License-Identifier: MIT

// Package registry ships Hydra's routing rules inside the binary.
//
// These YAML files used to be read from disk only. That works from a repo
// checkout and fails everywhere else: goreleaser archives just the binary plus
// LICENSE/NOTICE/README/CHANGELOG, and brew, npm, pip and curl all install the
// binary alone, so every real install ran with no registry at all. Tier prices
// silently became $0.00, agy heads silently vanished, and the deployment
// breadcrumb silently stopped being stamped (#238).
//
// Embedding is what makes the files reachable from an installed binary; adding
// them to the archive would not, because the package managers never copy them
// anywhere ScriptHome() looks.
//
// The Go file lives in the same directory as the YAML on purpose: go:embed
// cannot reach outside its own package directory, and CLAUDE.md points
// operators at registry/routing.yaml as the place to change tier assignments.
// Moving the data to make embedding tidier would invalidate that.
package registry

import (
	"embed"
	"os"
	"path/filepath"
)

//go:embed *.yaml
var embedded embed.FS

// Read returns the contents of registry file name (e.g. "pricing.yaml").
//
// A copy on disk at home/registry/name wins, so operators can still edit
// routing rules without rebuilding, that is the whole point of the registry
// being YAML. The embedded copy is the fallback, which is what an installed
// binary uses.
func Read(home, name string) ([]byte, error) {
	if home != "" {
		if raw, err := os.ReadFile(filepath.Join(home, "registry", name)); err == nil {
			return raw, nil
		}
	}
	return embedded.ReadFile(name)
}

// Names lists the registry files compiled into the binary.
func Names() []string {
	entries, err := embedded.ReadDir(".")
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}
