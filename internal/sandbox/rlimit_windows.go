// SPDX-License-Identifier: MIT

//go:build windows

package sandbox

import (
	"errors"
	"os/exec"
)

// RlimitExecArg, RlimitMemEnv and RlimitCPUEnv mirror the Unix constants so
// cmd/hydra's hidden subcommand needs no build-tag branching of its own.
const (
	RlimitExecArg = "__rlimit-exec"
	RlimitMemEnv  = "HYDRA_RLIMIT_MEM_MB"
	RlimitCPUEnv  = "HYDRA_RLIMIT_CPU_SECONDS"
)

// WithLimits is a deliberate no-op on Windows: there is no rlimit
// equivalent, only Job Objects, a different mechanism this codebase already
// leaves unimplemented for the same reason Harden's process-group kill does
// on this platform (#738). cmd is returned unchanged rather than claiming a
// ceiling that was never applied.
func WithLimits(cmd *exec.Cmd, memMB, cpuSeconds int) error {
	return nil
}

// RunRlimitExec is never reached on Windows: WithLimits never rewrites a
// command to invoke it. Present only so cmd/hydra's hidden subcommand
// compiles unconditionally.
func RunRlimitExec(argv []string) error {
	return errors.New("rlimit-exec: not supported on windows")
}
