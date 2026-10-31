// SPDX-License-Identifier: MIT

//go:build !windows

package util

import (
	"io/fs"
	"syscall"
)

// DiskBytes reports bytes actually charged on disk rather than logical length.
// The gap is the whole point wherever small files are involved: a 344-byte file
// occupies a whole 4 KiB block. st_blocks is in 512-byte units by POSIX
// convention regardless of the filesystem's block size.
func DiskBytes(info fs.FileInfo) int64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Blocks > 0 {
		return int64(st.Blocks) * 512
	}
	return info.Size()
}
