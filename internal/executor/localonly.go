// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"fmt"
	"strings"

	"github.com/ankit373/hydra/internal/gateway"
	"github.com/ankit373/hydra/internal/provider"
)

// proxyVars are every spelling a subprocess might read for its outbound
// proxy. All are stripped from env before the gateway's own address is
// appended, or a duplicate key added after an inherited one is undefined
// behaviour: some libc getenv implementations return the first match, so an
// appended override can silently lose to the corporate proxy it was meant to
// replace.
var proxyVars = []string{
	"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
	"http_proxy", "https_proxy", "all_proxy", "no_proxy",
}

// gateIfLocalOnly starts a gateway.Gateway and rewrites env to route through
// it when h is LocalOnly, closing the gap internal/security's Boundary report
// otherwise has to describe as an unverified claim. Every other head is
// unaffected: env comes back unchanged, and the returned closer is a no-op.
//
// The caller must defer the returned func after checking err, and only after
// the subprocess this env is for has exited (Close does not wait on a
// hijacked tunnel already in flight).
func gateIfLocalOnly(ctx context.Context, h provider.Head, env []string) ([]string, func(), error) {
	if !h.LocalOnly {
		return env, func() {}, nil
	}
	gw, err := gateway.Start(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("local-only gateway for %q: %w", h.ID, err)
	}
	return proxyEnv(env, gw.Addr()), func() { gw.Close() }, nil
}

// proxyEnv strips any inherited proxy variable from env and points every
// spelling a subprocess might check at addr instead.
func proxyEnv(env []string, addr string) []string {
	out := make([]string, 0, len(env)+len(proxyVars))
	for _, kv := range env {
		key, _, ok := strings.Cut(kv, "=")
		if ok && isProxyVar(key) {
			continue
		}
		out = append(out, kv)
	}
	url := "http://" + addr
	for _, k := range proxyVars {
		if strings.HasPrefix(k, "NO_") || strings.HasPrefix(k, "no_") {
			continue // nothing bypasses the gate, not even loopback: Allowed already permits it
		}
		out = append(out, k+"="+url)
	}
	return out
}

func isProxyVar(key string) bool {
	for _, k := range proxyVars {
		if key == k {
			return true
		}
	}
	return false
}
