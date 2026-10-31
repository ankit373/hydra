// SPDX-License-Identifier: MIT

// Package sandbox bounds the subprocesses Hydra spawns: what they can see in
// their environment, and whether they can outlive the call that started them.
//
// It is not a sandbox in the kernel sense. Go's os/exec has no per-child
// rlimit support, and faking one through `sh -c 'ulimit ...; exec'` would
// re-tokenize argv, which internal/oracle deliberately avoids. Platform
// confinement (sandbox-exec, seccomp) is a separate piece of work; what is
// here is the portable part that needs no privileges.
package sandbox

import (
	"os"
	"runtime"
	"strings"
)

// passthrough is what every subprocess inherits: enough to find binaries, a
// home directory to read its own config from, and a working locale. Anything
// not named here does not cross, which is what stops one head from reading
// credentials meant for a different provider.
var passthrough = []string{
	"PATH", "HOME", "USER", "LOGNAME", "SHELL",
	"LANG", "LC_ALL", "LC_CTYPE", "TERM", "TMPDIR", "TZ",

	// A corporate proxy is the difference between a head working and not, and
	// excluding these breaks the network entirely for those users. They can
	// carry credentials in the URL, which is a real if uncommon leak, and the
	// alternative is worse.
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
	"http_proxy", "https_proxy", "no_proxy",

	// Windows will not start a process without these.
	"SYSTEMROOT", "SYSTEMDRIVE", "WINDIR", "PATHEXT", "COMSPEC",
	"TEMP", "TMP", "USERPROFILE", "APPDATA", "LOCALAPPDATA",
	"NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE",
}

// macOS-only. Absent it, some Foundation-linked tools mangle non-ASCII output.
const darwinTextEncoding = "__CF_USER_TEXT_ENCODING"

// BaseEnv is the environment a subprocess starts from: the passthrough set,
// and nothing else.
//
// Deliberately excluded and worth naming, because each was previously
// inherited by every head Hydra spawned: every provider API key, AWS and Azure
// credentials, and SSH_AUTH_SOCK, which is agent forwarding and therefore a
// live credential of its own.
func BaseEnv() []string {
	out := make([]string, 0, len(passthrough)+1)
	for _, k := range passthrough {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	if runtime.GOOS == "darwin" {
		if v, ok := os.LookupEnv(darwinTextEncoding); ok {
			out = append(out, darwinTextEncoding+"="+v)
		}
	}
	return out
}

// WithVars returns BaseEnv plus the named variables, for the few a particular
// tool legitimately needs. A name that is unset, or whose value is empty, is
// skipped rather than exported as an empty string: a tool that checks
// os.Getenv("X") != "" behaves the same either way, but one that checks
// LookupEnv does not, and an empty key reads as configured-but-broken.
func WithVars(names ...string) []string {
	env := BaseEnv()
	for _, k := range names {
		if k == "" {
			continue
		}
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			env = append(env, k+"="+os.Getenv(k))
		}
	}
	return env
}
