// SPDX-License-Identifier: MIT

// Package build exposes version metadata injected at build time via -ldflags.
package build

// Set by goreleaser ldflags; fallback values used for local/dev builds.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
	BuiltBy = "source"
)
