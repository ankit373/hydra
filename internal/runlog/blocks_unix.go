// SPDX-License-Identifier: MIT

//go:build !windows

package runlog

import (
	"io/fs"
	"syscall"
)

// blocksFor reports bytes actually charged on disk, which is what makes the
// one-file-per-run cost visible: a 344-byte run occupies a whole 4 KiB block.
// st_blocks is in 512-byte units by POSIX convention regardless of block size.
func blocksFor(info fs.FileInfo) int64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Blocks > 0 {
		return int64(st.Blocks) * 512
	}
	return info.Size()
}
