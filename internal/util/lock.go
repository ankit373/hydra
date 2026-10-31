// SPDX-License-Identifier: MIT

package util

import "os"

// FileLock serializes a read-modify-write across processes. A sync.Mutex only
// covers goroutines in one process; the ledger's chainhash and the payload
// store's pack offsets both need to hold across separate `hyctl` processes
// too, hence an OS-level advisory lock (flock/LockFileEx).
type FileLock struct {
	f *os.File
}

// Lock opens (creating if needed) the sidecar lock file at path and blocks
// until it holds an exclusive lock on it.
func Lock(path string) (*FileLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockExclusive(f); err != nil {
		f.Close()
		return nil, err
	}
	return &FileLock{f: f}, nil
}

// Unlock releases the lock and closes the file. Closing releases the OS-level
// lock even if unlockExclusive itself fails, so a panic between the two never
// leaves a store permanently wedged.
func (l *FileLock) Unlock() error {
	defer l.f.Close()
	return unlockExclusive(l.f)
}

// LockPath is the sidecar lock file for a data file.
func LockPath(path string) string { return path + ".lock" }
