// SPDX-License-Identifier: MIT

//go:build !windows

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
// It deliberately does NOT set Setpgid. Putting a head in its own process
// group is what would let a timeout kill the whole tree, but Hydra installs no
// SIGINT handler anywhere, so the shell's Ctrl+C would then reach Hydra and
// not the head: the terminal would return while agy kept running. That trades
// a rare orphan on timeout for a routine orphan on Ctrl+C, which is worse.
// Process-group isolation needs signal handling wired first.
func Harden(cmd *exec.Cmd) *exec.Cmd {
	cmd.WaitDelay = waitDelay
	return cmd
}
