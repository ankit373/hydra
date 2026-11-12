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

// heartbeat claims the run's beat file and touches it until the returned stop
// is called, which also removes it. Safe to call stop more than once.
//
// The claim is exclusive, which is what refuses a second runner: reading a
// status and then acting on it lets two resumes in the same instant both pass
// the read and double-dispatch every remaining step (#1162).
func heartbeat(id string) (func(), error) {
	path, err := beatPath(id)
	if err != nil {
		// Unsaveable anyway; Save is what reports a malformed id.
		return func() {}, nil
	}
	// The store directory is created by the first Save, which happens *after*
	// Run starts beating, so without this the first touch lands in a directory
	// that does not exist yet and the run reads as interrupted until the first
	// tick, seconds later.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return func() {}, nil
	}
	if err := takeBeat(path); err != nil {
		return nil, err
	}

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
	}, nil
}

// takeBeat creates the beat file exclusively, taking over one whose writer has
// stopped beating. A beat still fresh belongs to a runner that is alive, and a
// second runner over it would dispatch every remaining step again (#1162).
func takeBeat(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		return f.Close()
	}
	if !os.IsExist(err) {
		// A beat that cannot be written makes a live run read as interrupted,
		// the safe direction; it must not stop work, as in touch below.
		return nil
	}
	if freshBeat(path) {
		return ErrAlreadyRunning
	}
	// Stale: that writer is gone. Take it over, and concede if another runner
	// won the same race.
	_ = os.Remove(path)
	if f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err != nil {
		if os.IsExist(err) {
			return ErrAlreadyRunning
		}
		return nil
	}
	return f.Close()
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
	return freshBeat(path)
}

func freshBeat(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	return time.Since(fi.ModTime()) < beatTimeout
}
