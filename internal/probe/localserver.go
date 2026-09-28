// SPDX-License-Identifier: MIT

package probe

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/security"
)

// scanTimeout bounds one identity probe. A server that cannot answer this fast
// on loopback is not one a dispatch would have waited for either.
const scanTimeout = 2 * time.Second

// identityPath is the unauthenticated endpoint that says which server this is,
// and where one exists, what version it is. Every path here is one the port
// provider already relies on to identify that service, so the two cannot come
// to different conclusions about what is listening.
var identityPath = map[string]struct {
	path       string
	versionKey string // "" where the server publishes no version
}{
	"ollama":   {"/api/version", "version"},
	"llamacpp": {"/props", "build_info"},
	"lmstudio": {"/v1/models", ""},
	"litellm":  {"/health/liveliness", ""},
}

// ScanLocalServers asks each local model server behind the discovered heads
// what version it is and whether it answers on an address other than loopback.
//
// Deliberately not part of Discover: `hyctl probe` is on the dispatch path and
// must not grow an HTTP call per service, which is the cost #750 removed.
func ScanLocalServers(ctx context.Context, heads []provider.Head) []security.LocalServer {
	targets := map[string]string{} // endpoint -> kind
	for _, h := range heads {
		kind, _, ok := strings.Cut(h.ID, "/")
		if !ok || h.Endpoint == "" {
			continue
		}
		if _, known := identityPath[kind]; known {
			targets[h.Endpoint] = kind
		}
	}
	if len(targets) == 0 {
		return nil
	}

	off := offHostAddrs()
	out := make([]security.LocalServer, 0, len(targets))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for endpoint, kind := range targets {
		wg.Add(1)
		go func(endpoint, kind string) {
			defer wg.Done()
			s := scanOne(ctx, endpoint, kind, off)
			mu.Lock()
			out = append(out, s)
			mu.Unlock()
		}(endpoint, kind)
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].Endpoint < out[j].Endpoint })
	return out
}

func scanOne(ctx context.Context, endpoint, kind string, off []string) security.LocalServer {
	s := security.LocalServer{Endpoint: endpoint, Kind: kind}
	identity, version := identify(ctx, endpoint, kind)
	s.Version = version

	u, err := url.Parse(endpoint)
	if err != nil {
		return s
	}
	// An endpoint already pointing somewhere other than loopback needs no
	// probe: it is the answer. $OLLAMA_HOST set to a LAN address is the common
	// way to arrive here, and it is exposure by configuration rather than by
	// accident.
	if host := u.Hostname(); host != "" && !isLoopbackHost(host) {
		s.Tried, s.OffHost = 1, host
		return s
	}
	// Without an identity from loopback there is nothing to compare against,
	// so a match off-host could not be established either way.
	if identity == "" {
		return s
	}
	// Concurrently, because a dropped packet costs the whole deadline and a
	// machine with several interfaces would otherwise pay that once per
	// address: 4s on a laptop with five, in a command people run often.
	matched := make([]bool, len(off))
	var wg sync.WaitGroup
	for i, addr := range off {
		wg.Add(1)
		go func(i int, addr string) {
			defer wg.Done()
			alt := *u
			alt.Host = net.JoinHostPort(addr, u.Port())
			other, _ := identify(ctx, alt.String(), kind)
			matched[i] = other == identity
		}(i, addr)
	}
	wg.Wait()

	s.Tried = len(off)
	// off is sorted, so the reported address is the same one twice rather than
	// whichever goroutine happened to finish first.
	for i, ok := range matched {
		if ok {
			s.OffHost = off[i]
			break
		}
	}
	return s
}

// identify returns a stable fingerprint of the server answering at base, and
// its version where it publishes one. The fingerprint is what makes an off-host
// answer evidence about *this* server: a TCP connect would prove only that
// something listens on that port.
func identify(ctx context.Context, base, kind string) (identity, version string) {
	route, ok := identityPath[kind]
	if !ok {
		return "", ""
	}
	ctx, cancel := context.WithTimeout(ctx, scanTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+route.path, nil)
	if err != nil {
		return "", ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", ""
	}
	if route.versionKey != "" {
		var fields map[string]any
		if json.Unmarshal(body, &fields) == nil {
			if v, isString := fields[route.versionKey].(string); isString {
				version = strings.TrimSpace(v)
			}
		}
	}
	// The whole body is the fingerprint where no version is published: a model
	// list identifies a server as well as a version string does, and better
	// than a port number does.
	return strings.TrimSpace(string(body)), version
}

// offHostAddrs returns this machine's own non-loopback addresses. Connecting to
// one of these is handled by the local stack and puts no packet on the wire.
// Link-local is skipped: it needs a zone to dial and adds no case loopback and
// a routable address do not already cover.
func offHostAddrs() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() || ipNet.IP.IsLinkLocalUnicast() || ipNet.IP.IsMulticast() {
			continue
		}
		out = append(out, ipNet.IP.String())
	}
	sort.Strings(out)
	return out
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
