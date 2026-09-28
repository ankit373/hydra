// SPDX-License-Identifier: MIT

package executor

import (
	"os"
	"strings"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/sandbox"
)

// toolEnv names the non-credential variables a particular CLI tool reads for
// its own configuration. Endpoints and project ids, not secrets, but a head
// pointed at a custom base URL stops working without them.
var toolEnv = map[string][]string{
	"anthropic":   {"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CONFIG_DIR"},
	"openai":      {"OPENAI_BASE_URL", "OPENAI_ORG_ID", "CODEX_HOME"},
	"google":      {"GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_PROJECT", "GEMINI_CONFIG_DIR"},
	"antigravity": {"AGY_TIMEOUT", "AGY_CONFIG_DIR"},
	"github":      {"GH_TOKEN", "GITHUB_TOKEN", "GH_HOST"},
	"sourcegraph": {"SRC_ENDPOINT", "SRC_ACCESS_TOKEN"},
	"cursor":      {"CURSOR_CONFIG_DIR"},
}

// extraEnvVar lets an operator restore a variable this package does not know
// a head needs. Comma-separated names, e.g. HYDRA_HEAD_ENV=FOO_TOKEN,BAR_URL.
//
// It exists because the allowlist cannot be complete: every CLI agent reads
// its own settings and they change. Without an escape hatch the fix for one
// unlisted variable is to stop using Hydra.
const extraEnvVar = "HYDRA_HEAD_ENV"

// headEnv is the environment a head subprocess runs with: the base set, that
// provider's own credential, its tool configuration, and nothing else.
//
// Every head used to inherit the full environment, so an agy subprocess held
// AWS_SECRET_ACCESS_KEY, OPENAI_API_KEY and every other provider's credential
// for the duration of the call, alongside SSH_AUTH_SOCK. Least privilege here
// is per provider, using the same map that already decides which credential an
// HTTP head sends.
func headEnv(h provider.Head) []string {
	names := make([]string, 0, 8)
	names = append(names, apiKeyEnvs[h.Provider]...)
	names = append(names, toolEnv[h.Provider]...)
	for _, k := range strings.Split(os.Getenv(extraEnvVar), ",") {
		if k = strings.TrimSpace(k); k != "" {
			names = append(names, k)
		}
	}
	return sandbox.WithVars(names...)
}
