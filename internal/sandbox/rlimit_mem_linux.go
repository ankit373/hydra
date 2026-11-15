// SPDX-License-Identifier: MIT

//go:build linux

package sandbox

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

// prepareExec builds everything execve needs now: once RLIMIT_AS sits below this
// process's own mappings, any allocation is fatal. memBytes 0 means no memory cap.
func prepareExec(target string, argv, env []string, memBytes uint64) (func() error, error) {
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
	limit := &syscall.Rlimit{Cur: memBytes, Max: memBytes}
	return func() error {
		runtime.LockOSThread()
		// Raw and adjacent so this thread runs no Go code, and maps nothing, between the cap
		// and the exec. EPERM means an ambient limit is already at or below the one asked for.
		if memBytes > 0 {
			_, _, errno := syscall.RawSyscall6(syscall.SYS_PRLIMIT64, 0, syscall.RLIMIT_AS,
				uintptr(unsafe.Pointer(limit)), 0, 0, 0)
			if errno != 0 && errno != syscall.EPERM {
				return fmt.Errorf("set memory limit: %w", errno)
			}
		}
		_, _, errno := syscall.RawSyscall(syscall.SYS_EXECVE,
			uintptr(unsafe.Pointer(path)),
			uintptr(unsafe.Pointer(&args[0])),
			uintptr(unsafe.Pointer(&envp[0])))
		return errno
	}, nil
}
