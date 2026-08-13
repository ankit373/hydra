// SPDX-License-Identifier: MIT

package ledger

import "os"

// chainLock serializes Record's chainhash read-modify-write. A sync.Mutex
// only covers goroutines in one process; this needs to hold across separate
// `hyctl` processes too, hence an OS-level advisory lock (flock/LockFileEx).
type chainLock struct {
	f *os.File
}

// lockChain opens (creating if needed) the sidecar lock file at path and
// blocks until it holds an exclusive lock on it.
func lockChain(path string) (*chainLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockExclusive(f); err != nil {
		f.Close()
		return nil, err
	}
	return &chainLock{f: f}, nil
}

// unlock releases the lock and closes the file. Closing releases the OS-level
// lock even if unlockExclusive itself fails, so a panic between the two never
// leaves the ledger permanently wedged.
func (l *chainLock) unlock() error {
	defer l.f.Close()
	return unlockExclusive(l.f)
}

func lockPath(ledgerPath string) string { return ledgerPath + ".lock" }
