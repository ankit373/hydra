// SPDX-License-Identifier: MIT

//go:build linux

package sandbox

import (
	"syscall"
	"unsafe"
)

// setMemLimit bounds the process's virtual address space. Linux enforces
// RLIMIT_AS as documented: the next allocation past bytes fails rather than
// the kernel reclaiming what is already mapped.
func setMemLimit(bytes uint64) error {
	return syscall.Setrlimit(syscall.RLIMIT_AS, &syscall.Rlimit{Cur: bytes, Max: bytes})
}

// prepareExec builds execve's arrays now: syscall.Exec allocates them, and once
// RLIMIT_AS sits below this process's own mappings that allocation is fatal.
func prepareExec(target string, argv, env []string) (func() error, error) {
	path, err := syscall.BytePtrFromString(target)
	if err != nil {
		return nil, err
	}
	args, err := syscall.SlicePtrFromStrings(argv)
	if err != nil {
		return nil, err
	}
	envp, err := syscall.SlicePtrFromStrings(env)
	if err != nil {
		return nil, err
	}
	return func() error {
		_, _, errno := syscall.RawSyscall(syscall.SYS_EXECVE,
			uintptr(unsafe.Pointer(path)),
			uintptr(unsafe.Pointer(&args[0])),
			uintptr(unsafe.Pointer(&envp[0])))
		return errno
	}, nil
}
