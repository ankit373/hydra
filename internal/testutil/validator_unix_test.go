// SPDX-License-Identifier: MIT

//go:build !windows

package testutil

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The validator hands over a live child's pid, and killing its group ends both.
func TestHangingValidator_HandsOverALiveChild(t *testing.T) {
	tmpl, fifo := HangingValidator(t)
	script := strings.Fields(tmpl)[0]

	cmd := exec.Command(script, "unused")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })

	never := make(chan struct{})
	child := AwaitValidator(t, fifo, never, nil)
	if ProcessGone(child, 100*time.Millisecond) {
		t.Fatalf("child %d is gone before anything killed it", child)
	}

	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Wait()
	if !ProcessGone(child, 5*time.Second) {
		t.Errorf("child %d outlived its process group", child)
	}
}

// The stall message names the stage that never ran, before and after the head answers.
func TestHeadAnswered_NamesTheStageThatStalled(t *testing.T) {
	s := NewSandbox(t)
	s.FakeBinary(t, "cody", EchoScript("ok"))
	stalled := HeadAnswered(t, "cody")

	if got := stalled(); !strings.Contains(got, "head never answered") {
		t.Errorf("before the head ran: %q", got)
	}

	head, err := exec.LookPath("cody")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(head).CombinedOutput(); err != nil {
		t.Fatalf("fake head failed: %v\n%s", err, out)
	}
	if got := stalled(); !strings.Contains(got, "validator never started") {
		t.Errorf("after the head ran: %q", got)
	}
}

func TestProcessGone_KeepsWaitingOnALiveProcess(t *testing.T) {
	if ProcessGone(os.Getpid(), 50*time.Millisecond) {
		t.Error("the test's own process was reported gone")
	}
}
