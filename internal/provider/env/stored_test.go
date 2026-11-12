// SPDX-License-Identifier: MIT

package env

import (
	"context"
	"os"
	"testing"

	"github.com/ankit373/hydra/internal/keystore"
	"github.com/ankit373/hydra/internal/testutil"
)

// A key saved in the app must reach discovery with no shell export at all.
func TestLoadStoredKeys_StoredKeyBecomesAHead(t *testing.T) {
	testutil.NewSandbox(t)
	if err := keystore.Set("GROQ_API_KEY", "gsk-stored-0123456789"); err != nil {
		t.Fatal(err)
	}
	if err := LoadStoredKeys(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { keystore.Unload("GROQ_API_KEY") })
	if os.Getenv("GROQ_API_KEY") != "" {
		t.Error("the stored key entered the environment, where every subprocess inherits it")
	}
	heads, err := (&Provider{}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(heads) != 1 || heads[0].ID != "env/groq" {
		t.Fatalf("heads = %+v, want env/groq from the stored key", heads)
	}
}

// An exported variable is the more deliberate statement, so it wins.
func TestLoadStoredKeys_ExportWins(t *testing.T) {
	s := testutil.NewSandbox(t)
	s.SetKey(t, "OPENAI_API_KEY", "sk-from-shell")
	if err := keystore.Set("OPENAI_API_KEY", "sk-from-store-0123456789"); err != nil {
		t.Fatal(err)
	}
	if err := LoadStoredKeys(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { keystore.Unload("OPENAI_API_KEY") })
	if got := keystore.Lookup("OPENAI_API_KEY"); got != "sk-from-shell" {
		t.Errorf("OPENAI_API_KEY = %q, the stored key replaced the exported one", got)
	}
}

// The store is a file a user can edit; only provider key names may leave it.
func TestLoadStoredKeys_ExportsOnlyProviderKeys(t *testing.T) {
	testutil.NewSandbox(t)
	t.Setenv("HYDRA_TEST_INJECTED", "")
	if err := keystore.Set("HYDRA_TEST_INJECTED", "x0123456789"); err != nil {
		t.Fatal(err)
	}
	if err := LoadStoredKeys(); err != nil {
		t.Fatal(err)
	}
	if got := keystore.Lookup("HYDRA_TEST_INJECTED"); got != "" {
		t.Errorf("a non-provider name was exported from the store: %q", got)
	}
}

func TestLoadStoredKeys_UnreadableStoreIsReported(t *testing.T) {
	s := testutil.NewSandbox(t)
	if err := os.WriteFile(s.HydraHome+"/api_keys.json", []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadStoredKeys(); err == nil {
		t.Error("a corrupt key store loaded as empty")
	}
}

func TestKeyVars_SingleKeyProvidersOnly(t *testing.T) {
	got := map[string]string{}
	for _, kv := range KeyVars() {
		got[kv.Provider] = kv.Var
	}
	if got["google"] != "GEMINI_API_KEY" || got["anthropic"] != "ANTHROPIC_API_KEY" {
		t.Errorf("KeyVars = %v", got)
	}
	for _, p := range []string{"bedrock", "azure"} {
		if _, ok := got[p]; ok {
			t.Errorf("%s needs more than one value and cannot be a pasted key", p)
		}
	}
}
