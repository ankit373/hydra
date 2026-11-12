// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ankit373/hydra/internal/keystore"
	"github.com/ankit373/hydra/internal/sandbox"
)

const storedKey = "sk-ant-stored-0123456789abcdef"

// anthropicAnswering stands in for the Anthropic API and answers only when
// the request carries storedKey, so a pass proves which key was sent.
func anthropicAnswering(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != storedKey {
			http.Error(w, `{"error":{"type":"authentication_error"}}`, http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-test\",\"usage\":{\"input_tokens\":3}}}\n\n")
			fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"from the stored key\"}}\n\n")
			fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":4}}\n\n")
			fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"model":"claude-test","content":[{"type":"text","text":"from the stored key"}],"usage":{"input_tokens":3,"output_tokens":4}}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
}

// A key saved in the app, with nothing exported, is what `hyctl dispatch`
// authenticates with.
func TestCLI_Dispatch_UsesAStoredKey(t *testing.T) {
	cliSandbox(t)
	anthropicAnswering(t)
	if err := keystore.Set("ANTHROPIC_API_KEY", storedKey); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { keystore.Unload("ANTHROPIC_API_KEY") })
	out, cobraOut, err := run(t, "dispatch", "say something")
	if err != nil {
		t.Fatalf("dispatch failed: %v\n%s%s", err, out, cobraOut)
	}
	if !strings.Contains(out+cobraOut, "from the stored key") {
		t.Errorf("the answer did not come from the stored key's head:\n%s%s", out, cobraOut)
	}
}

// rlimit-exec execs a head immediately; loading keys there would only open
// the keychain once per head for nothing.
func TestCLI_RlimitExec_DoesNotLoadStoredKeys(t *testing.T) {
	cliSandbox(t)
	if err := keystore.Set("ANTHROPIC_API_KEY", storedKey); err != nil {
		t.Fatal(err)
	}

	var rlimit *cobra.Command
	for _, c := range rootCmd().Commands() {
		if c.Name() == sandbox.RlimitExecArg {
			rlimit = c
		}
	}
	if rlimit == nil || rlimit.PersistentPreRun == nil {
		t.Fatal("rlimit-exec inherits the root's PersistentPreRun")
	}
	rlimit.PersistentPreRun(rlimit, nil)
	if keystore.Lookup("ANTHROPIC_API_KEY") != "" {
		t.Error("rlimit-exec loaded the stored keys")
	}
}
