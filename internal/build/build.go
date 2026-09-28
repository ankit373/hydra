// SPDX-License-Identifier: MIT

// Package build exposes version metadata injected at build time via -ldflags.
package build

import "fmt"

// Set by goreleaser ldflags; fallback values used for local/dev builds.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
	BuiltBy = "source"
)

// Text renders the stamp for one binary. Shared, so two binaries shipped in one
// archive cannot report their build differently (#1058).
func Text(binary string) string {
	return fmt.Sprintf("  %s %s\n  commit:  %s\n  built:   %s\n  by:      %s\n",
		binary, Version, Commit, Date, BuiltBy)
}
