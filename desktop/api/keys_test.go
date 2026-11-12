// SPDX-License-Identifier: MIT

package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/health"
	"github.com/ankit373/hydra/internal/keystore"
	"github.com/ankit373/hydra/internal/provider/env"
	"github.com/ankit373/hydra/internal/testutil"
)

const testKey = "sk-ant-desktop-0123456789abcdWXYZ"

// anthropicStub answers like the Anthropic API when the request carries
// testKey, and with status otherwise. echo puts the key in the error body, the
// way a careless proxy might, to prove the app never shows it.
func anthropicStub(t *testing.T, status int, echo bool) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != testKey || status != http.StatusOK {
			body := `{"error":"denied"}`
			if echo {
				body = `{"error":"bad key ` + r.Header.Get("x-api-key") + `"}`
			}
			http.Error(w, body, status)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-test\",\"usage\":{\"input_tokens\":3}}}\n\n")
			fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ready\"}}\n\n")
			fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":1}}\n\n")
			fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"model":"claude-test","content":[{"type":"text","text":"ready"}],"usage":{"input_tokens":3,"output_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
}

// keysSandbox is testutil's sandbox plus a cleanup of every key this process
// was told came from the store, a record that outlives t.Setenv's restore.
func keysSandbox(t *testing.T) *testutil.Sandbox {
	t.Helper()
	s := testutil.NewSandbox(t)
	t.Cleanup(func() {
		for _, kv := range env.KeyVars() {
			keystore.Unload(kv.Var)
		}
	})
	return s
}

func keyFor(t *testing.T, ks ProviderKeys, provider string) ProviderKey {
	t.Helper()
	for _, k := range ks.Keys {
		if k.Provider == provider {
			return k
		}
	}
	t.Fatalf("no %s row in %+v", provider, ks.Keys)
	return ProviderKey{}
}

// Every provider a key can enable needs a name a person recognises and the
// page that issues the key, or the row is a variable name and nothing else.
func TestProviderKeys_EveryProviderIsNamedAndLinked(t *testing.T) {
	keysSandbox(t)
	ks := New().GetProviderKeys()
	if len(ks.Keys) != len(env.KeyVars()) {
		t.Fatalf("%d rows for %d providers", len(ks.Keys), len(env.KeyVars()))
	}
	for _, k := range ks.Keys {
		if k.Label == "" || !strings.HasPrefix(k.KeyURL, "https://") {
			t.Errorf("%s has no label or key page: %+v", k.Provider, k)
		}
		if k.Source != "" {
			t.Errorf("%s reads as configured on a fresh machine", k.Provider)
		}
	}
	if ks.Store == "" {
		t.Error("Store is empty, so 'saved' has no meaning on screen")
	}
}

// The key goes in once and never comes back out: only its last four
// characters cross to the frontend.
func TestSaveProviderKey_StoresAndNeverReturnsTheKey(t *testing.T) {
	keysSandbox(t)
	a := New()

	ks := a.SaveProviderKey("anthropic", "  "+testKey+"\n")
	if ks.Error != "" {
		t.Fatal(ks.Error)
	}
	k := keyFor(t, ks, "anthropic")
	if k.Source != "stored" || k.Hint != "WXYZ" {
		t.Errorf("row = %+v, want stored with hint WXYZ", k)
	}
	if got := keystore.Lookup("ANTHROPIC_API_KEY"); got != testKey {
		t.Errorf("the saved key is not live in this process: %q", got)
	}
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		t.Error("the saved key entered the environment, where every subprocess inherits it")
	}
	wire, _ := json.Marshal(ks)
	if strings.Contains(string(wire), testKey) {
		t.Error("the key itself crossed to the frontend")
	}
	if stored, _ := keystore.Get("ANTHROPIC_API_KEY"); stored != testKey {
		t.Errorf("stored %q", stored)
	}

	ks = a.RemoveProviderKey("anthropic")
	if k := keyFor(t, ks, "anthropic"); k.Source != "" || k.Hint != "" {
		t.Errorf("after removal the row still reads %+v", k)
	}
	if keystore.Lookup("ANTHROPIC_API_KEY") != "" {
		t.Error("the removed key is still live in this process")
	}
}

// A key exported outside Hydra is the more deliberate statement: saving one
// here does not replace it, and removing the saved one does not unset it.
func TestProviderKeys_AnExportedKeyWins(t *testing.T) {
	s := keysSandbox(t)
	s.SetKey(t, "OPENAI_API_KEY", "sk-exported-0123456789-ABCD")
	a := New()

	k := keyFor(t, a.SaveProviderKey("openai", "sk-saved-0123456789-EFGH"), "openai")
	if k.Source != "environment" || k.Hint != "ABCD" || !k.Saved {
		t.Errorf("row = %+v, want the exported key reported and the saved one removable", k)
	}
	a.RemoveProviderKey("openai")
	if os.Getenv("OPENAI_API_KEY") != "sk-exported-0123456789-ABCD" {
		t.Error("removing the saved key unset the exported one")
	}
}

func TestSaveProviderKey_RefusalsNeverQuoteTheKey(t *testing.T) {
	keysSandbox(t)
	a := New()

	if ks := a.SaveProviderKey("nope", testKey); ks.Error == "" {
		t.Error("an unknown provider was accepted")
	}
	if ks := a.RemoveProviderKey("nope"); ks.Error == "" {
		t.Error("an unknown provider was accepted for removal")
	}
	ks := a.SaveProviderKey("anthropic", "sk-ant has a space")
	if ks.Error == "" || strings.Contains(ks.Error, "sk-ant has a space") {
		t.Errorf("Error = %q, want a refusal that does not repeat the key", ks.Error)
	}
	if keystore.Lookup("ANTHROPIC_API_KEY") != "" {
		t.Error("a refused key went live")
	}
}

func TestTestProviderKey(t *testing.T) {
	cases := []struct {
		name   string
		status int
		echo   bool
		ok     bool
		taken  bool
		want   string
	}{
		{"accepted", http.StatusOK, false, true, true, ""},
		{"rejected", http.StatusUnauthorized, false, false, false, "rejected this key"},
		{"no credit", http.StatusTooManyRequests, false, false, true, "out of credit"},
		{"echoed key", http.StatusBadRequest, true, false, false, "[your key]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			keysSandbox(t)
			anthropicStub(t, tc.status, tc.echo)
			a := New()
			a.SaveProviderKey("anthropic", testKey)

			got := a.TestProviderKey("anthropic")
			if got.OK != tc.ok || got.Accepted != tc.taken || !strings.Contains(got.Error, tc.want) {
				t.Errorf("TestProviderKey = %+v", got)
			}
			if tc.ok && got.Reply != "ready" {
				t.Errorf("Reply = %q", got.Reply)
			}
			if strings.Contains(got.Error, testKey) {
				t.Error("the key appeared in the error shown to the user")
			}
		})
	}
}

func TestTestProviderKey_NeedsAKeyAndAKnownProvider(t *testing.T) {
	keysSandbox(t)
	a := New()
	if got := a.TestProviderKey("anthropic"); got.OK || got.Error == "" {
		t.Errorf("tested with no key: %+v", got)
	}
	if got := a.TestProviderKey("nope"); got.OK || got.Error == "" {
		t.Errorf("tested an unknown provider: %+v", got)
	}
	// Replicate has no default model, so a key alone cannot be tried.
	a.SaveProviderKey("replicate", "r8_0123456789abcdefghij")
	if got := a.TestProviderKey("replicate"); got.OK || got.Error == "" {
		t.Errorf("replicate tested with no model: %+v", got)
	}
}

// The whole first run with no terminal: no config, no hyctl, a key pasted in
// the app, then a request answered by the head that key enables.
func TestFirstRun_PasteAKeyThenAsk(t *testing.T) {
	s := keysSandbox(t)
	anthropicStub(t, http.StatusOK, false)
	a := New()

	if r, _ := a.Chat("hello", "", "", "", ""); !r.NeedsProbe {
		t.Fatalf("with no key the reply should ask for a model: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(s.HydraHome, "config.toml")); err != nil {
		t.Fatalf("Chat did not write the default config: %v", err)
	}
	a.SaveProviderKey("anthropic", testKey)

	r, err := a.Chat("Summarise this in one line.", "", "", "", "")
	if err != nil || r.Error != "" {
		t.Fatalf("Chat = %+v, %v", r, err)
	}
	if r.Output != "ready" || r.Head != "env/anthropic" {
		t.Errorf("answered by %q with %q", r.Head, r.Output)
	}
}

// A config that is there and does not parse holds the only record of what was
// meant; first-run setup must not write over it (#1030).
func TestEnsureConfig_LeavesAnUnreadableFileAlone(t *testing.T) {
	s := keysSandbox(t)
	path := filepath.Join(s.HydraHome, "config.toml")
	if err := os.WriteFile(path, []byte("cortex = ["), 0o600); err != nil {
		t.Fatal(err)
	}
	r, _ := New().Chat("hello", "", "", "", "")
	if r.Error == "" {
		t.Error("an unreadable config produced no error")
	}
	if b, _ := os.ReadFile(path); string(b) != "cortex = [" {
		t.Errorf("the unreadable config was rewritten: %q", b)
	}
	if _, err := config.Load(); err == nil {
		t.Error("config now parses, so it was replaced")
	}
}

func TestHint_ShortKeysRevealNothing(t *testing.T) {
	if got := hint("abcd1234"); got != "" {
		t.Errorf("hint of a short key = %q", got)
	}
	if got := redact("nothing to hide", ""); got != "nothing to hide" {
		t.Errorf("redact with no key = %q", got)
	}
}

// Google is discovered from either variable, so the panel and the test must
// read both, or a working head shows as not added.
func TestProviderKeys_GoogleReadsEitherVariable(t *testing.T) {
	s := keysSandbox(t)
	s.SetKey(t, "GOOGLE_API_KEY", "AIza-exported-0123456789-GOOG")
	a := New()
	if k := keyFor(t, a.GetProviderKeys(), "google"); k.Source != "environment" || k.Hint != "GOOG" {
		t.Errorf("row = %+v, want the exported GOOGLE_API_KEY reported", k)
	}
	if got := a.TestProviderKey("google"); got.Error == "Add a key first." {
		t.Error("the test ignored the key discovery uses")
	}
}

// A key the index lists but this process could not load is reported, not
// shown as saved and working.
func TestProviderKeys_AnUnreadableSavedKeyIsReported(t *testing.T) {
	keysSandbox(t)
	if err := keystore.Set("MISTRAL_API_KEY", "mistral-test-xxxxxxxxxxxxxxxx"); err != nil {
		t.Fatal(err)
	}
	ks := New().GetProviderKeys()
	if !strings.Contains(ks.Error, "could not read the saved Mistral key") {
		t.Errorf("Error = %q", ks.Error)
	}
}

// A head parked for failing with an earlier key is cleared once a key works.
func TestTestProviderKey_ClearsAParkedHead(t *testing.T) {
	keysSandbox(t)
	anthropicStub(t, http.StatusOK, false)
	hs := health.Open(health.DefaultPath())
	for i := 0; i < 5; i++ {
		hs.Fail("env/anthropic", "401", health.Fatal, time.Now())
	}
	if err := hs.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, parked := health.Open(health.DefaultPath()).Blocked("env/anthropic", time.Now()); !parked {
		t.Fatal("setup did not park the head, so this test proves nothing")
	}
	a := New()
	a.SaveProviderKey("anthropic", testKey)
	if got := a.TestProviderKey("anthropic"); !got.OK {
		t.Fatalf("test failed: %+v", got)
	}
	if reason, parked := health.Open(health.DefaultPath()).Blocked("env/anthropic", time.Now()); parked {
		t.Errorf("the head is still parked after its key worked: %s", reason)
	}
}
