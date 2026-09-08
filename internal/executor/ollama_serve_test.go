// SPDX-License-Identifier: MIT

package executor

import "testing"

// The finding: `ollama serve` inherited Hydra's environment and reads
// $OLLAMA_HOST itself. provider.OllamaHost already refuses a non-loopback
// cleartext host for Hydra's own requests and falls back to localhost, so with
// OLLAMA_HOST=0.0.0.0 Hydra would start a model server bound to every
// interface while believing it was talking to loopback.
func TestServeTarget_OnlyEverBindsLoopback(t *testing.T) {
	for _, c := range []struct {
		host string
		want string
		ok   bool
	}{
		{"http://localhost:11434", "localhost:11434", true},
		{"http://127.0.0.1:11434", "127.0.0.1:11434", true},
		{"http://[::1]:11434", "[::1]:11434", true},

		// The exposure. Hydra must refuse to start a server here, not bind wide.
		{"http://0.0.0.0:11434", "", false},
		{"http://192.168.1.5:11434", "", false},
		{"https://ollama.example.com", "", false},

		// A prefix match would pass this: it is a hostname someone else owns
		// and points wherever they like. Same reasoning as provider.isLoopback.
		{"http://127.0.0.1.evil.com:11434", "", false},

		{"", "", false},
		{"not a url at all", "", false},
	} {
		got, ok := serveTarget(c.host)
		if ok != c.ok || got != c.want {
			t.Errorf("serveTarget(%q) = (%q, %v), want (%q, %v)", c.host, got, ok, c.want, c.ok)
		}
	}
}
