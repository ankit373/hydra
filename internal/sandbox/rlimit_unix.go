// SPDX-License-Identifier: MIT

//go:build linux || darwin

// syscall.Rlimit's field type varies across the wider Unix family (int64 on
// some BSDs, uint64 here), so this file is scoped to the two Unix targets
// Hydra actually ships rather than claiming portability it was never tested
// against; rlimit_other_unix.go covers every other Unix with an honest no-op.
package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// RlimitExecArg is the hidden hyctl subcommand argument that turns a process
// into the self-re-exec wrapper WithLimits builds.
const RlimitExecArg = "__rlimit-exec"

// RlimitMemEnv and RlimitCPUEnv carry the limits from WithLimits to
// RunRlimitExec across the exec boundary, since a rewritten argv has nowhere
// else to put them without disturbing the target's own flags.
const (
	RlimitMemEnv = "HYDRA_RLIMIT_MEM_MB"
	RlimitCPUEnv = "HYDRA_RLIMIT_CPU_SECONDS"
)

// WithLimits rewrites cmd to run through a self-re-exec wrapper when either
// limit is positive; 0 or negative means no ceiling for that resource, the
// same spelling max_wall_seconds already uses in policy.yaml.
//
// The caller must set cmd.Env to the full desired child environment before
// calling this: it appends the limit variables, and appending onto a nil
// cmd.Env would replace "inherit os.Environ()" with just these two
// variables instead of the environment the caller meant the child to have,
// so a nil cmd.Env here is refused rather than silently narrowed.
//
// syscall.Exec (execve) rather than a shell wrapper: it replaces the process
// image with no re-tokenized argv and no fork, so it inherits stdio for free
// and keeps the same pid, which is what lets sandbox.Harden's process-group
// kill keep working underneath this unchanged.
func WithLimits(cmd *exec.Cmd, memMB, cpuSeconds int) error {
	if memMB <= 0 && cpuSeconds <= 0 {
		return nil
	}
	if cmd.Env == nil {
		return fmt.Errorf("sandbox.WithLimits: cmd.Env must be set before calling, got nil")
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve hyctl's own path for rlimit wrapper: %w", err)
	}

	// The original argv, argv[0] included: RunRlimitExec resolves and execs
	// it exactly as given, bare name or absolute path alike, so there is
	// nothing to preserve here beyond passing it through unchanged.
	realArgv := cmd.Args

	cmd.Path = self
	cmd.Args = append([]string{self, RlimitExecArg, "--"}, realArgv...)

	if memMB > 0 {
		cmd.Env = append(cmd.Env, RlimitMemEnv+"="+strconv.Itoa(memMB))
	}
	if cpuSeconds > 0 {
		cmd.Env = append(cmd.Env, RlimitCPUEnv+"="+strconv.Itoa(cpuSeconds))
	}
	return nil
}

// RunRlimitExec is what the hidden RlimitExecArg subcommand calls: apply the
// limits named in the environment, then execve into argv, the real target
// and its own arguments exactly as they would have run without this wrapper.
func RunRlimitExec(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("rlimit-exec: no target command after --")
	}

	if v := os.Getenv(RlimitMemEnv); v != "" {
		mb, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("rlimit-exec: %s=%q: %w", RlimitMemEnv, v, err)
		}
		if mb > 0 {
			if err := setMemLimit(uint64(mb) * 1024 * 1024); err != nil && !errors.Is(err, syscall.EPERM) {
				// EPERM means an ambient limit (container, systemd unit) is
				// already at or below what was asked: you cannot raise a
				// hard limit without privilege, and an environment that
				// refuses to raise it is, by construction, already at least
				// this restrictive. Any other error is refused.
				return fmt.Errorf("rlimit-exec: set memory limit: %w", err)
			}
		}
	}

	if v := os.Getenv(RlimitCPUEnv); v != "" {
		sec, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("rlimit-exec: %s=%q: %w", RlimitCPUEnv, v, err)
		}
		if sec > 0 {
			limit := &syscall.Rlimit{Cur: uint64(sec), Max: uint64(sec)}
			if err := syscall.Setrlimit(syscall.RLIMIT_CPU, limit); err != nil && !errors.Is(err, syscall.EPERM) {
				return fmt.Errorf("rlimit-exec: set cpu limit: %w", err)
			}
		}
	}

	target := argv[0]
	if !strings.Contains(target, "/") {
		resolved, err := exec.LookPath(target)
		if err != nil {
			return fmt.Errorf("rlimit-exec: look up %q: %w", target, err)
		}
		target = resolved
	}

	// The target's environment is this process's own, minus the two
	// variables that exist only to cross the exec boundary: least exposure,
	// the same reasoning internal/executor's headEnv already applies to
	// every credential a head does not need.
	env := stripRlimitEnv(os.Environ())

	// Does not return on success: the process image is replaced in place, so
	// everything after this line only runs when the exec itself failed.
	err := syscall.Exec(target, argv, env)
	return fmt.Errorf("rlimit-exec: exec %q: %w", target, err)
}

func stripRlimitEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, RlimitMemEnv+"=") || strings.HasPrefix(kv, RlimitCPUEnv+"=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}
