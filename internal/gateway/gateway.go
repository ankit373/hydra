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
package gateway

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// errDenied marks a destination that resolved fine but classified as not
// loopback/private, as opposed to one Hydra could not classify at all. Both
// deny, but only the second is a real error worth surfacing to a caller.
var errDenied = errors.New("destination is not loopback or private")

// Gateway is a loopback-bound forward proxy: everything through it is
// classified before a byte reaches the target.
type Gateway struct {
	l   net.Listener
	srv *http.Server

	// Resolve looks up a hostname's addresses. nil uses net.DefaultResolver;
	// tests inject a fake so classification never depends on real DNS.
	Resolve func(ctx context.Context, host string) ([]net.IP, error)

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

	dialAddr, err := resolveSafe(r.Context(), hostport, g.Resolve)
	if err != nil {
		http.Error(w, "forbidden: destination is not loopback or private", http.StatusForbidden)
		return
	}

	if r.Method == http.MethodConnect {
		g.tunnel(w, r, dialAddr)
		return
	}
	g.forward(w, r, dialAddr)
}

// tunnel serves a CONNECT: dial the address resolveSafe already classified,
// hijack the client connection, then relay bytes in both directions until
// either side closes.
func (g *Gateway) tunnel(w http.ResponseWriter, r *http.Request, dialAddr string) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}
	// dialAddr, not r.Host: resolveSafe already resolved the hostname once.
	// Resolving it again here would let a DNS answer that changes between
	// the two lookups pass the check on one address and connect to another,
	// a name it never actually classified.
	target, err := (&net.Dialer{}).DialContext(r.Context(), "tcp", dialAddr)
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
// already classified as safe to dial directly.
var directTransport = &http.Transport{Proxy: nil}

// forward serves a plain (non-CONNECT) proxied request, dialing the address
// resolveSafe already classified rather than letting the transport resolve
// the hostname again, the same rebinding gap tunnel avoids. The original
// Host header is preserved so a name-based virtual host still resolves
// correctly at the target even though the connection dials its IP directly.
func (g *Gateway) forward(w http.ResponseWriter, r *http.Request, dialAddr string) {
	outReq := r.Clone(r.Context())
	outReq.RequestURI = "" // set on every server Request; RoundTrip rejects it on a client one
	outReq.URL.Host = dialAddr
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

// Allowed classifies hostport (host, or host:port), true only if every
// address it names is loopback, private (RFC1918/RFC4193), or link-local.
// resolve is used for a name that isn't already a literal IP; nil uses
// net.DefaultResolver.
//
// A resolution failure or an empty answer denies rather than passing through:
// an address Hydra could not classify is not evidence it is safe. A name that
// resolves to a mix of private and public addresses denies too, on the same
// reasoning a single public one would.
func Allowed(ctx context.Context, hostport string, resolve func(context.Context, string) ([]net.IP, error)) (bool, error) {
	_, err := resolveSafe(ctx, hostport, resolve)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, errDenied):
		return false, nil
	default:
		return false, err
	}
}

// resolveSafe classifies hostport and, when every candidate address is
// loopback/private/link-local, returns the specific address to dial. A
// caller must dial exactly this address rather than resolving hostport a
// second time: a hostname that resolves differently between the check and
// the dial (a hostile or merely low-TTL DNS answer) would otherwise pass the
// gate on one address and connect to another it never classified.
func resolveSafe(ctx context.Context, hostport string, resolve func(context.Context, string) ([]net.IP, error)) (string, error) {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		host, port = hostport, ""
	}

	if ip := net.ParseIP(host); ip != nil {
		if !isLocalIP(ip) {
			return "", errDenied
		}
		return joinHostPort(ip.String(), port), nil
	}

	if resolve == nil {
		resolve = defaultResolve
	}
	ips, err := resolve(ctx, host)
	if err != nil {
		return "", err
	}
	if len(ips) == 0 {
		return "", errors.New("gateway: no addresses resolved for " + host)
	}
	for _, ip := range ips {
		if !isLocalIP(ip) {
			return "", errDenied
		}
	}
	return joinHostPort(ips[0].String(), port), nil
}

func joinHostPort(host, port string) string {
	if port == "" {
		return host
	}
	return net.JoinHostPort(host, port)
}

func isLocalIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

func defaultResolve(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, len(addrs))
	for i, a := range addrs {
		ips[i] = a.IP
	}
	return ips, nil
}
