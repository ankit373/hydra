// SPDX-License-Identifier: MIT

//go:build windows

package ledger

import (
	"os"

	"golang.org/x/sys/windows"
)

// allBytes locks/unlocks the whole file: LockFileEx takes a byte range, and
// there's no "whole file" sentinel other than the maximum range.
const allBytes = ^uint32(0)

func lockExclusive(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, allBytes, allBytes, ol)
}

func unlockExclusive(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, allBytes, allBytes, ol)
}
