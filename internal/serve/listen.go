// SPDX-License-Identifier: MIT

// Package serve exposes Hydra's router as an OpenAI-compatible endpoint, so a
// tool that speaks OpenAI routes through the same policy, fallback and spend
// logging a `hyctl dispatch` does.
package serve

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

// ErrExposed is the refusal to bind an unauthenticated port off-loopback.
var ErrExposed = errors.New("refusing to serve an unauthenticated port off loopback")

// Listen binds addr, refusing an off-loopback bind that has no token.
//
// A refusal rather than a warning. `hyctl security` reports when another local
// model server answers off-loopback, on the grounds that Ollama ships with no
// authentication and says nothing when bound to 0.0.0.0. Hydra serving the same
// way by default would make that check describe a risk Hydra itself created.
func Listen(addr, token string) (net.Listener, error) {
	if !loopback(addr) && token == "" {
		return nil, fmt.Errorf("%w: %s reaches other machines, and this endpoint spends money and reads your code. "+
			"Set --token (or HYDRA_SERVE_TOKEN), or bind 127.0.0.1", ErrExposed, addr)
	}
	return net.Listen("tcp", addr)
}

// loopback reports whether addr can only be reached from this machine. An
// unparseable or hostless address is treated as exposed: the safe reading of
// something it cannot verify.
func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return false
	}
	if host == "" {
		return false // ":8080" binds every interface
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
