// SPDX-License-Identifier: MIT

//go:build windows

package runlog

import "io/fs"

// blocksFor falls back to logical size on Windows, where allocation size is not
// exposed through fs.FileInfo. Sealing still recovers the same space; only the
// reported saving is understated.
func blocksFor(info fs.FileInfo) int64 { return info.Size() }
