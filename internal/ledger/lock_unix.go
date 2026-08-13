// SPDX-License-Identifier: MIT

//go:build !windows

package ledger

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockExclusive blocks until it holds an exclusive flock on f, retrying on
// EINTR — a flock interrupted by a signal must be retried, not treated as
// lock failure.
func lockExclusive(f *os.File) error {
	fd := int(f.Fd())
	for {
		err := unix.Flock(fd, unix.LOCK_EX)
		if err != unix.EINTR {
			return err
		}
	}
}

func unlockExclusive(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
