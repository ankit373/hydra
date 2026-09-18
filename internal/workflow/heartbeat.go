// SPDX-License-Identifier: MIT

package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// A run's liveness is a heartbeat file the running process touches, not the
// status it last stored: a process that dies leaves the last thing it wrote
// standing, and a reader repeating that says "running" about nothing (#898).
//
// A file's mtime rather than a pid, because a pid is reused by unrelated
// processes, and rather than the record's own Updated, because a long step is
// alive the whole time it produces nothing to save.
const (
	beatInterval = 5 * time.Second
	// Three intervals: one missed beat is a slow disk, three is a dead process.
	beatTimeout = 3 * beatInterval
)

// beatPath is the run's heartbeat file, beside its record rather than inside
// it, so a reader never has to write to learn whether a writer is alive.
func beatPath(id string) (string, error) {
	path, err := Path(id)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(path, ".json") + ".alive", nil
}

// heartbeat touches the run's beat file until the returned stop is called,
// which also removes it. Safe to call stop more than once.
func heartbeat(id string) func() {
	path, err := beatPath(id)
	if err != nil {
		return func() {}
	}
	// The store directory is created by the first Save, which happens *after*
	// Run starts beating, so without this the first touch lands in a directory
	// that does not exist yet and the run reads as interrupted until the first
	// tick, seconds later.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return func() {}
	}
	touch(path)

	done := make(chan struct{})
	go func() {
		t := time.NewTicker(beatInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				touch(path)
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			_ = os.Remove(path)
		})
	}
}

// touch updates the mtime, creating the file the first time. Both failures are
// ignored on purpose: a heartbeat that cannot be written makes a live run read
// as interrupted, which is the safe direction, where failing the run would stop
// work over a file nothing depends on.
func touch(path string) {
	now := time.Now()
	if err := os.Chtimes(path, now, now); err == nil {
		return
	}
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_ = f.Close()
	}
}

// alive reports whether a run's heartbeat is recent enough to believe.
func alive(id string) bool {
	path, err := beatPath(id)
	if err != nil {
		return false
	}
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	return time.Since(fi.ModTime()) < beatTimeout
}
