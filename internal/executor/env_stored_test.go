// SPDX-License-Identifier: MIT

package executor

import (
	"testing"

	"github.com/ankit373/hydra/internal/keystore"
	"github.com/ankit373/hydra/internal/provider"
)

// A key saved in the app enables the HTTP head. Handed to the claude CLI it
// would switch that tool from the user's own login to API billing.
func TestHeadEnv_WithholdsAStoredKeyFromACLIHead(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	keystore.Load("ANTHROPIC_API_KEY", "sk-ant-from-the-store")
	t.Cleanup(func() { keystore.Unload("ANTHROPIC_API_KEY") })

	env := envMap(t, headEnv(provider.Head{ID: "claude", Provider: "anthropic"}))
	if v, ok := env["ANTHROPIC_API_KEY"]; ok {
		t.Errorf("a stored key reached the CLI head: %q", v)
	}
	if apiKeyFor("anthropic") != "sk-ant-from-the-store" {
		t.Error("the HTTP head can no longer read the stored key")
	}
}
