// SPDX-License-Identifier: MIT

//go:build linux

package sandbox

import "syscall"

// setMemLimit bounds the process's virtual address space. Linux enforces
// RLIMIT_AS as documented: the next allocation past bytes fails rather than
// the kernel reclaiming what is already mapped.
func setMemLimit(bytes uint64) error {
	return syscall.Setrlimit(syscall.RLIMIT_AS, &syscall.Rlimit{Cur: bytes, Max: bytes})
}
