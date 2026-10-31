// SPDX-License-Identifier: MIT

//go:build darwin

package sandbox

// setMemLimit is a deliberate no-op on Darwin: verified against this
// platform's kernel, syscall.Setrlimit(RLIMIT_AS, ...) and (RLIMIT_DATA, ...)
// both fail with EINVAL, not merely go unenforced, so there is no memory
// ceiling this technique can apply here. cpuSeconds still applies: Darwin
// does honour RLIMIT_CPU, so RunRlimitExec's caller loses only the memory
// half of a combined request, not the whole ceiling.
func setMemLimit(bytes uint64) error { return nil }
