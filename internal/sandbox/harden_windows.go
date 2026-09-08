// SPDX-License-Identifier: MIT

//go:build windows

package sandbox

import (
	"os/exec"
	"time"
)

// waitDelay bounds how long Wait blocks after the process is gone. A
// grandchild inheriting stdout keeps the pipe open, and without this the call
// hangs on a process that already exited.
const waitDelay = 5 * time.Second

// Harden bounds a subprocess without changing who can signal it. Killing a
// tree on Windows means a job object, which needs the same signal wiring the
// Unix build is waiting on. See harden_unix.go.
func Harden(cmd *exec.Cmd) *exec.Cmd {
	cmd.WaitDelay = waitDelay
	return cmd
}
