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

// Harden bounds a subprocess without changing who can signal it.
//
// The Unix build isolates the head's process group so a cancelled run kills
// its helpers too. Windows has no equivalent: it needs a job object, which is
// a different mechanism and cannot be exercised from CI's build-only Windows
// job, so a timed-out head still leaves helpers behind here (#738). Console
// Ctrl+C reaches the child either way, since nothing asks for a new group.
func Harden(cmd *exec.Cmd) *exec.Cmd {
	cmd.WaitDelay = waitDelay
	return cmd
}
