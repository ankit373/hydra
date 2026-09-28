// SPDX-License-Identifier: MIT

//go:build !windows

package sandbox

import (
	"os/exec"
	"syscall"
	"time"
)

// waitDelay bounds how long Wait blocks after the process is gone. A
// grandchild inheriting stdout keeps the pipe open, and without this the call
// hangs on a process that already exited.
const waitDelay = 5 * time.Second

// Harden bounds a subprocess and puts it in a process group of its own, so
// cancelling a run kills the whole tree rather than the direct child.
//
// Every CLI agent spawns helpers, and exec.CommandContext signals only the
// child it started, leaving them running and still holding the pipe. The group
// also takes the head out of the terminal's foreground group, so Ctrl+C no
// longer reaches it: cmd/hydra catches SIGINT and tears down through the
// context instead, which is why that had to land first (#738).
func Harden(cmd *exec.Cmd) *exec.Cmd {
	cmd.WaitDelay = waitDelay
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// A negative pid signals the group. Setpgid with no Pgid makes the
		// child its own group leader, so -pid is exactly this head's tree.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != syscall.ESRCH {
			return err
		}
		return nil // already gone: cancellation got what it wanted
	}
	return cmd
}
