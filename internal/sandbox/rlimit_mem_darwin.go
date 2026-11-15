// SPDX-License-Identifier: MIT

//go:build darwin

package sandbox

import "syscall"

// prepareExec ignores memBytes on purpose: Darwin's setrlimit fails RLIMIT_AS and
// RLIMIT_DATA with EINVAL, so there is no memory cap to apply. RLIMIT_CPU still works.
func prepareExec(target string, argv, env []string, memBytes uint64) (func() error, error) {
	return func() error { return syscall.Exec(target, argv, env) }, nil
}
