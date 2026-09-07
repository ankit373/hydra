// SPDX-License-Identifier: MIT

package util

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestLockPath_IsASidecar(t *testing.T) {
	if got := LockPath("/a/b/blobs.idx"); got != "/a/b/blobs.idx.lock" {
		t.Fatalf("LockPath = %q", got)
	}
}

func TestLock_CreatesTheFileAndReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.lock")
	l, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file was not created: %v", err)
	}
	if err := l.Unlock(); err != nil {
		t.Fatal(err)
	}
	// Releasing must leave the lock takeable again, or the first writer wedges
	// the store for every later one.
	l2, err := Lock(path)
	if err != nil {
		t.Fatalf("lock could not be retaken after release: %v", err)
	}
	if err := l2.Unlock(); err != nil {
		t.Fatal(err)
	}
}

// The point of the lock is that the second holder waits. A lock that never
// blocks is not a lock, and the failure it permits (interleaved appends into
// one pack) is silent.
func TestLock_SecondHolderWaitsForTheFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.lock")
	first, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}

	var acquired atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		second, err := Lock(path)
		if err != nil {
			t.Error(err)
			return
		}
		acquired.Store(true)
		_ = second.Unlock()
	}()

	// Long enough that a non-blocking lock would have been taken by now.
	time.Sleep(100 * time.Millisecond)
	if acquired.Load() {
		t.Fatal("the second holder took the lock while the first still held it")
	}
	if err := first.Unlock(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the second holder never acquired the lock after release")
	}
	if !acquired.Load() {
		t.Fatal("the second holder finished without acquiring")
	}
}

// An unopenable path must report an error rather than returning a lock that
// guards nothing.
func TestLock_FailsOnAnUnopenablePath(t *testing.T) {
	dir := t.TempDir()
	if _, err := Lock(filepath.Join(dir, "missing-dir", "store.lock")); err == nil {
		t.Fatal("Lock succeeded on a path it cannot create")
	}
}
