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
	"os/exec"
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

// toolchainVars are what common build and test tools read to find their
// caches and installs. Paths and settings only; a validator needs nothing else.
var toolchainVars = []string{
	"GOROOT", "GOPATH", "GOCACHE", "GOMODCACHE", "GOFLAGS", "GOPROXY",
	"GOPRIVATE", "GONOSUMDB", "GOTOOLCHAIN", "CGO_ENABLED",
	"NODE_PATH", "npm_config_cache", "NPM_CONFIG_CACHE",
	"VIRTUAL_ENV", "PYTHONPATH", "CARGO_HOME", "RUSTUP_HOME", "JAVA_HOME",
	"XDG_CACHE_HOME", "XDG_CONFIG_HOME",
}

// credentialWords and credentialPrefixes mark a name as a credential. Such a
// name never reaches a validator, even when the config asks for it.
var (
	credentialWords    = []string{"KEY", "TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL", "AUTH"}
	credentialPrefixes = []string{"AWS_", "AZURE_", "ARM_", "GOOGLE_", "GCP_", "GCLOUD_", "CLOUDSDK_"}
)

// IsCredentialName reports whether an environment variable name looks like it
// holds a credential, e.g. OPENAI_API_KEY, AWS_PROFILE or SSH_AUTH_SOCK.
func IsCredentialName(name string) bool {
	up := strings.ToUpper(name)
	for _, p := range credentialPrefixes {
		if strings.HasPrefix(up, p) {
			return true
		}
	}
	for _, w := range credentialWords {
		if strings.Contains(up, w) {
			return true
		}
	}
	return false
}

// ValidatorEnv is BaseEnv plus toolchain vars plus extra, for commands that run
// model-written files. Extra names IsCredentialName flags are dropped into refused.
func ValidatorEnv(extra []string) (env, refused []string) {
	names := append([]string{}, toolchainVars...)
	for _, k := range extra {
		if k = strings.TrimSpace(k); k == "" {
			continue
		}
		if IsCredentialName(k) {
			refused = append(refused, k)
			continue
		}
		names = append(names, k)
	}
	return WithVars(names...), refused
}

// Confine hardens cmd and gives it ValidatorEnv(extra). Every command that
// runs model-written files goes through this, never a bare exec.Command.
func Confine(cmd *exec.Cmd, extra []string) *exec.Cmd {
	cmd.Env, _ = ValidatorEnv(extra)
	return Harden(cmd)
}

// HeadEnvVar lets an operator restore a variable a head needs that the
// allowlist does not know. Comma-separated names, e.g. FOO_TOKEN,BAR_URL.
// The allowlist cannot be complete: every CLI agent reads its own settings.
const HeadEnvVar = "HYDRA_HEAD_ENV"

// HeadExtra returns the names HeadEnvVar lists, trimmed, blanks dropped.
func HeadExtra() []string {
	var names []string
	for _, k := range strings.Split(os.Getenv(HeadEnvVar), ",") {
		if k = strings.TrimSpace(k); k != "" {
			names = append(names, k)
		}
	}
	return names
}
