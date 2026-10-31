// SPDX-License-Identifier: MIT

//go:build windows

package util

import "io/fs"

// DiskBytes falls back to logical size on Windows, where allocation size is not
// exposed through fs.FileInfo. Callers still recover the same space; only the
// reported saving is understated.
func DiskBytes(info fs.FileInfo) int64 { return info.Size() }
