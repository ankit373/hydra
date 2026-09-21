// SPDX-License-Identifier: MIT

//go:build !windows && !linux && !darwin

package sandbox

import (
	"errors"
	"os/exec"
)

// RlimitExecArg, RlimitMemEnv and RlimitCPUEnv mirror the linux/darwin
// constants so callers need no build-tag branching of their own.
const (
	RlimitExecArg = "__rlimit-exec"
	RlimitMemEnv  = "HYDRA_RLIMIT_MEM_MB"
	RlimitCPUEnv  = "HYDRA_RLIMIT_CPU_SECONDS"
)

// WithLimits is a deliberate no-op here: Hydra ships only darwin, linux and
// windows (see .goreleaser.yaml), and syscall.Rlimit's field type is not
// even the same across the rest of the Unix family, so this file exists to
// keep an unshipped target building rather than to bound anything on it.
func WithLimits(cmd *exec.Cmd, memMB, cpuSeconds int) error {
	return nil
}

// RunRlimitExec is never reached here: WithLimits never rewrites a command
// to invoke it on this platform.
func RunRlimitExec(argv []string) error {
	return errors.New("rlimit-exec: not supported on this platform")
}
