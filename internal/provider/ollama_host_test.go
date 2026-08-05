// SPDX-License-Identifier: MIT

package provider

import (
	"testing"
)

// OllamaHost is the single answer both discovery and execution use. They used
// to disagree — the executor honoured $OLLAMA_HOST while the port provider
// hardcoded localhost, so a user running Ollama anywhere else had a working
// server discovery could not see, no tier-10 head, and a silent degrade to a
// paid one (#282).
func TestOllamaHost_Resolution(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want string
	}{{
		name: "unset uses the default",
		env:  "",
		want: DefaultOllamaHost,
	}, {
		// Ollama's own OLLAMA_HOST is a bare host:port; url.Parse would read
		// "192.168.1.5" as a scheme without the normalisation.
		name: "bare host:port is accepted the way ollama accepts it",
		env:  "127.0.0.1:11500",
		want: "http://127.0.0.1:11500",
	}, {
		name: "explicit http to loopback",
		env:  "http://localhost:9999",
		want: "http://localhost:9999",
	}, {
		name: "trailing slash is trimmed",
		env:  "http://localhost:11434/",
		want: "http://localhost:11434",
	}, {
		name: "a path is dropped, not appended to",
		env:  "http://localhost:11434/api/",
		want: "http://localhost:11434",
	}, {
		// A prompt is the user's source code. Sending it in cleartext to a
		// remote host because an environment variable said so is not a tradeoff
		// to make silently.
		name: "plain http to a remote host is refused",
		env:  "http://192.168.1.50:11434",
		want: DefaultOllamaHost,
	}, {
		name: "https to a remote host is allowed",
		env:  "https://ollama.example.com",
		want: "https://ollama.example.com",
	}, {
		name: "ipv6 loopback over http is allowed",
		env:  "http://[::1]:11434",
		want: "http://[::1]:11434",
	}, {
		name: "a non-http scheme falls back rather than erroring",
		env:  "ftp://localhost:11434",
		want: DefaultOllamaHost,
	}, {
		// A stray export must not make Ollama undiscoverable.
		name: "unparsable value falls back to the default",
		env:  "http://[::1",
		want: DefaultOllamaHost,
	}, {
		name: "a scheme with no host falls back",
		env:  "http://",
		want: DefaultOllamaHost,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("OLLAMA_HOST", tt.env)
			if got := OllamaHost(); got != tt.want {
				t.Errorf("OllamaHost() with OLLAMA_HOST=%q = %q, want %q", tt.env, got, tt.want)
			}
		})
	}
}

// isLoopback decides whether cleartext is acceptable, so a host that merely
// looks local must not qualify.
func TestIsLoopback(t *testing.T) {
	local := []string{"localhost", "::1", "127.0.0.1", "127.1.2.3"}
	for _, h := range local {
		if !isLoopback(h) {
			t.Errorf("isLoopback(%q) = false", h)
		}
	}
	remote := []string{
		"example.com",
		"192.168.1.5",
		"10.0.0.1",
		// Looks local, is not: a hostname someone else controls.
		"localhost.evil.com",
		"127.0.0.1.evil.com",
		"",
	}
	for _, h := range remote {
		if isLoopback(h) {
			t.Errorf("isLoopback(%q) = true — cleartext prompts would be sent there", h)
		}
	}
}
