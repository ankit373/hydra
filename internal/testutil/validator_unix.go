// SPDX-License-Identifier: MIT

//go:build !windows

package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// CancelBudget bounds how long a cancelled validator may take to return. The
// process-group kill is immediate; without it the caller blocks on the child's pipe.
const CancelBudget = 10 * time.Second

// DeadlineMargin is how far ahead of the test binary's deadline a wait gives up,
// so it fails with its own message instead of the binary's panic.
const DeadlineMargin = 5 * time.Second

// HangingValidator is a validator script that hands its child's pid through a
// fifo, then blocks far longer than any test waits. Absolute paths only (#738).
func HangingValidator(t *testing.T) (template, fifo string) {
	t.Helper()
	dir := t.TempDir()
	fifo = filepath.Join(dir, "helper.pid")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}

	script := filepath.Join(dir, "validate.sh")
	body := "#!/bin/sh\n" +
		"/bin/sleep 300 &\n" +
		"echo $! > " + fifo + "\n" +
		"/bin/sleep 300\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script + " {file}", fifo
}

// AwaitValidator blocks until the validator hands over its child's pid. A nil
// stalled means the validator is the only stage that can stall.
func AwaitValidator[T any](t *testing.T, fifo string, done <-chan T, stalled func() string) int {
	t.Helper()
	if stalled == nil {
		stalled = func() string { return "the validator never started" }
	}

	pid := make(chan string, 1)
	go func() {
		// The open blocks until the validator opens the fifo to write.
		raw, _ := os.ReadFile(fifo)
		pid <- strings.TrimSpace(string(raw))
	}()

	var expired <-chan time.Time
	deadline, ok := t.Deadline()
	if ok {
		expired = time.After(time.Until(deadline) - DeadlineMargin)
	}

	select {
	case raw := <-pid:
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			t.Fatalf("the validator handed over %q, not a pid", raw)
		}
		return n
	case got := <-done:
		t.Fatalf("returned before its validator started: %+v", got)
	case <-expired:
		t.Fatalf("the test binary's deadline is near: %s", stalled())
	}
	return 0
}

// HeadAnswered makes the fake head leave a marker once it has replied, so a
// stall can say whether the head or the validator never got there.
func HeadAnswered(t *testing.T, name string) func() string {
	t.Helper()
	head, err := exec.LookPath(name)
	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(head)
	if err != nil {
		t.Fatal(err)
	}

	marker := filepath.Join(t.TempDir(), "head.answered")
	touch := ": > " + marker + "\n"
	if err := os.WriteFile(head, append(script, touch...), 0o700); err != nil {
		t.Fatal(err)
	}

	return func() string {
		_, err := os.Stat(marker)
		if err != nil {
			return "the head never answered, so the edit never reached its validator"
		}
		return "the head answered, but the validator never started"
	}
}

// ProcessGone reports whether the pid has left the table within the given time.
// Signal 0 checks for existence without delivering anything.
func ProcessGone(pid int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
