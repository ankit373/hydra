// SPDX-License-Identifier: MIT

package provider

import (
	"net"
	"net/url"
	"os"
	"strings"
)

// DefaultOllamaHost is where Ollama listens unless told otherwise.
const DefaultOllamaHost = "http://localhost:11434"

// OllamaHost resolves where Ollama is listening, honouring $OLLAMA_HOST.
//
// This lives in provider, not executor, and not port, because both of them
// need it and neither should depend on the other. Discovery and execution
// having separate answers is exactly the bug: the executor honoured
// $OLLAMA_HOST while the port provider hardcoded localhost:11434, so a user
// running Ollama anywhere else had a working server that discovery could not
// see, no tier-10 head, and a silent degrade to a paid one (#282). Same shape
// as #248, two surfaces disagreeing about what exists.
//
// Plain http:// is restricted to loopback on purpose: a prompt is the user's
// source code, and sending it in cleartext to a remote host because an
// environment variable said so is not a tradeoff to make silently. https:// to
// a remote host is allowed. An unparsable or rejected value falls back to the
// default rather than erroring, so a stray export cannot make Ollama
// undiscoverable.
func OllamaHost() string {
	h := os.Getenv("OLLAMA_HOST")
	if h == "" {
		return DefaultOllamaHost
	}
	// Ollama itself accepts a bare "host:port"; url.Parse would read that as a
	// scheme, so normalise before parsing.
	if !strings.Contains(h, "://") {
		h = "http://" + h
	}
	u, err := url.Parse(h)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return DefaultOllamaHost
	}
	if u.Scheme == "http" && !isLoopback(u.Hostname()) {
		return DefaultOllamaHost
	}
	// Trailing slashes would produce "…//api/tags" once a path is appended.
	return strings.TrimRight(u.Scheme+"://"+u.Host, "/")
}

// isLoopback decides whether cleartext is acceptable for this host.
//
// It parses the address rather than matching a "127." prefix, because a prefix
// match is not an address check: "127.0.0.1.evil.com" is a perfectly valid
// hostname that someone else controls and that resolves wherever they point it,
// and it passed. The guard exists to keep the user's source code off the
// network in cleartext, so it has to test what the string actually is.
func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
