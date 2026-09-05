// SPDX-License-Identifier: MIT

// Package formats holds no code. It exists to own the contract tests for every
// file Hydra writes to or reads from disk.
//
// Those files are a public API the moment a second consumer exists, and there
// are already several: `hyctl cost`, `hyctl stats` and `hyctl trust` read them,
// the desktop app reads them, and users script against them with jq. A field
// rename in cost.jsonl silently breaks all three, and nothing else notices,
// the writer and the reader are usually the same struct, so a rename compiles
// and round-trips happily while every file already on disk stops parsing.
//
// The tests live here rather than beside each struct because the contract is
// about the *format*, not the package: what matters is that a file written by
// v1.1 still parses under v1.3, which is a statement about the shape on disk
// and not about any one Go type.
//
// See formats_test.go and testdata/.
package formats
