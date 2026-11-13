// SPDX-License-Identifier: MIT

// Package gateway is the enforcement internal/security's Boundary report says
// is missing: a LocalOnly head today is "a claim about the program itself
// rather than something Hydra verifies." This is that verification, a local
// forward proxy that denies anything outside loopback/private ranges.
//
// Deliberately no user-configurable allowlist: a local-only head only ever
// legitimately needs loopback or private destinations (Ollama, LM Studio,
// llama.cpp), so "deny everything else" is the whole policy, not a subset of
// one. It governs a head that honours HTTP_PROXY/HTTPS_PROXY, which every
// mainstream CLI/SDK does; a process that opens raw sockets and ignores the
// proxy environment is outside what this can see, same boundary
// internal/security already states for an opaque head in general.
//
// The gate never resolves a name. A DNS query carries the name to a server
// outside the machine whether or not the request is then refused, so
// resolving first would let a head leak data in the names it asks for.
package gateway

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// errDenied is the one reason the gate refuses a destination.
var errDenied = errors.New("destination is not loopback or private")

// Gateway is a loopback-bound forward proxy: everything through it is
// classified before a byte reaches the target.
type Gateway struct {
	l   net.Listener
	srv *http.Server

	mu      sync.Mutex
	tunnels map[net.Conn]struct{}
}

// Start binds an ephemeral loopback port and begins serving. The caller
// points a subprocess's HTTP_PROXY/HTTPS_PROXY at Addr() and calls Close when
// the subprocess exits; Close also fires on ctx cancellation, so a caller
// that forgets, or exits through a path that skips it, does not leak the
// listener or a live tunnel past its own context's lifetime.
func Start(ctx context.Context) (*Gateway, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	g := &Gateway{l: l, tunnels: make(map[net.Conn]struct{})}
	g.srv = &http.Server{Handler: http.HandlerFunc(g.serve)}
	go g.srv.Serve(l)
	go func() {
		<-ctx.Done()
		g.Close()
	}()
	return g, nil
}

// Addr is the proxy's own loopback host:port, suitable for
// "http://"+Addr() as HTTP_PROXY/HTTPS_PROXY.
func (g *Gateway) Addr() string { return g.l.Addr().String() }

// Close stops accepting and shuts the server down, force-closing any CONNECT
// tunnel still relaying.
//
// Shutdown alone does not wait for, or close, a hijacked connection (net/http's
// own documented limit); left alone a target that never closes its side of a
// tunnel leaks the relay goroutines for as long as the process runs. Gateway
// tracks each tunnel's connections itself and closes them here so Close is a
// real "stop", not merely "stop accepting new work."
func (g *Gateway) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := g.srv.Shutdown(ctx)

	g.mu.Lock()
	for c := range g.tunnels {
		c.Close()
	}
	g.mu.Unlock()

	return err
}

func (g *Gateway) track(c net.Conn)   { g.mu.Lock(); g.tunnels[c] = struct{}{}; g.mu.Unlock() }
func (g *Gateway) untrack(c net.Conn) { g.mu.Lock(); delete(g.tunnels, c); g.mu.Unlock() }

func (g *Gateway) serve(w http.ResponseWriter, r *http.Request) {
	hostport := r.Host
	if r.Method != http.MethodConnect && r.URL.Host != "" {
		hostport = r.URL.Host
	}

	targets, err := dialTargets(hostport)
	if err != nil {
		http.Error(w, "forbidden: destination is not loopback or private", http.StatusForbidden)
		return
	}

	if r.Method == http.MethodConnect {
		g.tunnel(w, r, targets)
		return
	}
	g.forward(w, r)
}

// tunnel serves a CONNECT: dial one of the literal addresses dialTargets
// returned, hijack the client connection, then relay bytes in both
// directions until either side closes.
func (g *Gateway) tunnel(w http.ResponseWriter, r *http.Request, targets []string) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}
	target, err := dialFirst(r.Context(), targets)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	client, _, err := hijacker.Hijack()
	if err != nil {
		target.Close()
		return
	}
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		client.Close()
		target.Close()
		return
	}
	g.relay(client, target)
}

// relay pipes both directions and returns once both have finished, so the
// caller never returns while a copy goroutine is still running against a
// connection it is about to leave open with nothing left to read it.
func (g *Gateway) relay(a, b net.Conn) {
	g.track(a)
	g.track(b)
	defer g.untrack(a)
	defer g.untrack(b)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); io.Copy(b, a); halfClose(b) }()
	go func() { defer wg.Done(); io.Copy(a, b); halfClose(a) }()
	wg.Wait()
	a.Close()
	b.Close()
}

// halfClose signals "no more data from me" without tearing down the other
// direction, which may still be relaying. Falls back to a full close on a
// connection type that offers no half-close.
func halfClose(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
		return
	}
	c.Close()
}

// directTransport never consults the process's own HTTP_PROXY/HTTPS_PROXY:
// http.DefaultTransport does, and Hydra itself may be running behind a
// corporate proxy, which would otherwise re-route a request this gateway
// already classified as safe to dial directly. Its dialer goes through
// dialTargets too, so the transport never resolves a name either.
var directTransport = &http.Transport{
	Proxy: nil,
	DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
		targets, err := dialTargets(addr)
		if err != nil {
			return nil, err
		}
		return dialFirst(ctx, targets)
	},
}

// forward serves a plain (non-CONNECT) proxied request through
// directTransport, which dials only the literal addresses dialTargets allows.
func (g *Gateway) forward(w http.ResponseWriter, r *http.Request) {
	outReq := r.Clone(r.Context())
	outReq.RequestURI = "" // set on every server Request; RoundTrip rejects it on a client one
	removeHopByHop(outReq.Header)

	resp, err := directTransport.RoundTrip(outReq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	removeHopByHop(resp.Header)
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

var hopByHopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Transfer-Encoding",
	"TE", "Trailer", "Upgrade", "Proxy-Authenticate", "Proxy-Authorization",
}

func removeHopByHop(h http.Header) {
	for _, k := range hopByHopHeaders {
		h.Del(k)
	}
}

// Allowed reports whether hostport (host, or host:port) is a destination
// the gate lets through: a loopback, private (RFC1918/RFC4193) or link-local
// IP literal, or a localhost name. It never consults DNS.
func Allowed(hostport string) bool {
	_, err := dialTargets(hostport)
	return err == nil
}

// dialTargets classifies hostport from its literal text and returns the
// addresses to dial, in order. Any name other than localhost is refused
// without a lookup, so the answer can never differ between check and dial.
func dialTargets(hostport string) ([]string, error) {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		host, port = strings.TrimSuffix(strings.TrimPrefix(hostport, "["), "]"), ""
	}

	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		if !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() {
			return nil, errDenied
		}
		return []string{joinHostPort(ip.String(), port)}, nil
	}

	if isLocalhost(host) {
		// RFC 6761 6.3: localhost names are loopback by definition. Both
		// families, since a local server may listen on only one of them.
		return []string{joinHostPort("127.0.0.1", port), joinHostPort("::1", port)}, nil
	}
	return nil, errDenied
}

// isLocalhost matches "localhost" and "*.localhost", with or without a
// trailing dot.
func isLocalhost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	return h == "localhost" || strings.HasSuffix(h, ".localhost")
}

// dialFirst returns a connection to the first address that accepts one.
func dialFirst(ctx context.Context, addrs []string) (net.Conn, error) {
	var d net.Dialer
	err := errDenied
	for _, a := range addrs {
		var c net.Conn
		if c, err = d.DialContext(ctx, "tcp", a); err == nil {
			return c, nil
		}
	}
	return nil, err
}

func joinHostPort(host, port string) string {
	if port == "" {
		return host
	}
	return net.JoinHostPort(host, port)
}
