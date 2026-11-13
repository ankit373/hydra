// SPDX-License-Identifier: MIT

package gateway

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAllowed(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		allowed bool
	}{
		{"ipv4 loopback", "127.0.0.1", true},
		{"ipv4 private 10/8", "10.1.2.3", true},
		{"ipv4 private 172.16/12", "172.16.0.1", true},
		{"ipv4 private 192.168/16", "192.168.1.1", true},
		{"ipv4 link-local", "169.254.1.1", true},
		{"ipv4 public", "8.8.8.8", false},
		{"ipv4 unspecified", "0.0.0.0", false},
		{"ipv6 loopback", "::1", true},
		{"ipv6 loopback bracketed", "[::1]", true},
		{"ipv6 unique-local", "fc00::1", true},
		{"ipv6 link-local with zone", "fe80::1%en0", true},
		{"ipv6 public", "2001:4860:4860::8888", false},
		{"ipv4-mapped public", "::ffff:8.8.8.8", false},
		{"ipv4 loopback with port", "127.0.0.1:9999", true},
		{"ipv4 public with port", "8.8.8.8:443", false},
		{"localhost", "localhost", true},
		{"localhost with port", "localhost:11434", true},
		{"localhost trailing dot", "LOCALHOST.", true},
		{"localhost subdomain", "ollama.localhost:8080", true},
		{"public name", "api.openai.com:443", false},
		{"mdns name", "ollama.local", false},
		{"localhost lookalike", "localhost.attacker.example", false},
		{"short-form ipv4", "127.1", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Allowed(tc.host); got != tc.allowed {
				t.Errorf("Allowed(%q) = %v, want %v", tc.host, got, tc.allowed)
			}
		})
	}
}

func proxyClient(t *testing.T, addr string) *http.Client {
	t.Helper()
	proxyURL, err := url.Parse("http://" + addr)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
}

func TestGateway_AllowsLoopback(t *testing.T) {
	g, err := Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)

	client := proxyClient(t, g.Addr())
	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("got status %d, want 200", resp.StatusCode)
	}
}

func TestGateway_TunnelsLoopbackTLS(t *testing.T) {
	g, err := Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })

	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)

	client := proxyClient(t, g.Addr())
	client.Transport.(*http.Transport).TLSClientConfig = ts.Client().Transport.(*http.Transport).TLSClientConfig
	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("got status %d, want 200", resp.StatusCode)
	}
}

func TestGateway_RejectsPublicHTTP(t *testing.T) {
	g, err := Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })

	client := proxyClient(t, g.Addr())
	// Denied before the gateway ever dials out, so this needs no real
	// network reachability to 8.8.8.8.
	resp, err := client.Get("http://8.8.8.8/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("got status %d, want 403", resp.StatusCode)
	}
}

func TestGateway_RejectsPublicConnect(t *testing.T) {
	g, err := Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })

	client := proxyClient(t, g.Addr())
	// The CONNECT itself is denied, so the client never gets to attempt a TLS
	// handshake with 8.8.8.8; no real network reachability required.
	_, err = client.Get("https://8.8.8.8/")
	if err == nil {
		t.Fatal("expected an error, got none")
	}
	if !strings.Contains(err.Error(), "403") && !strings.Contains(strings.ToLower(err.Error()), "forbidden") {
		t.Errorf("error %q does not mention the 403 denial", err)
	}
}

// A target that accepts and then never sends or closes must not pin a relay
// goroutine open past Close: Shutdown alone does not touch a hijacked
// connection, which is exactly what a CONNECT tunnel is.
func TestGateway_CloseClosesAHungTunnel(t *testing.T) {
	hung, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hung.Close() })
	go func() {
		// Accept and deliberately do nothing with the connection: the far
		// end of the tunnel, hanging exactly as a stuck local model would.
		hung.Accept()
	}()

	g, err := Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	conn, err := net.Dial("tcp", g.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", hung.Addr(), hung.Addr())
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT got status %d, want 200", resp.StatusCode)
	}

	if err := g.Close(); err != nil {
		t.Fatal(err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Error("expected the tunnel connection to be closed by Close, got a live read instead")
	}
}

// Start's ctx is not decorative: a caller relying on context cancellation
// rather than an explicit Close call must still see the listener come down.
func TestGateway_ClosesOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	g, err := Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	addr := g.Addr()
	cancel()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err != nil {
			return // listener is down, as expected
		}
		conn.Close()
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("gateway at %s still accepting connections after context cancellation", addr)
}

// recordDNS swaps net.DefaultResolver for one that counts every DNS query
// and answers none, so a lookup anywhere in the gate shows up here.
func recordDNS(t *testing.T) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	old := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			n.Add(1)
			return nil, errors.New("dns disabled in test")
		},
	}
	t.Cleanup(func() { net.DefaultResolver = old })
	return &n
}

// A refused name must never reach DNS: the query itself carries the name,
// so "<data>.attacker.example" would leak data even though the gate says 403.
func TestGateway_RefusedNameNeverReachesDNS(t *testing.T) {
	lookups := recordDNS(t)
	g, err := Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	client := proxyClient(t, g.Addr())

	resp, err := client.Get("http://c2VjcmV0.leak.example.com/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("plain: got status %d, want 403", resp.StatusCode)
	}
	if _, err := client.Get("https://c2VjcmV0.leak.example.com/"); err == nil {
		t.Error("CONNECT: expected the tunnel to be refused")
	}

	if n := lookups.Load(); n != 0 {
		t.Errorf("refused names caused %d DNS queries, want 0", n)
	}
}

// Rebinding needs a name resolved twice. An allowed name is mapped to
// loopback literals at both check and dial, so DNS is never asked at all.
func TestGateway_LocalhostDialsLiteralWithoutDNS(t *testing.T) {
	lookups := recordDNS(t)
	g, err := Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(r.Host))
	}))
	t.Cleanup(ts.Close)
	_, port, _ := net.SplitHostPort(ts.Listener.Addr().String())

	resp, err := proxyClient(t, g.Addr()).Get("http://localhost:" + port + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("got status %d, want 200", resp.StatusCode)
	}
	if n := lookups.Load(); n != 0 {
		t.Errorf("allowed localhost request caused %d DNS queries, want 0", n)
	}
}

// A local server bound only to ::1 is still reachable as localhost.
func TestDialTargets_LocalhostTriesIPv4ThenIPv6(t *testing.T) {
	got, err := dialTargets("localhost:8080")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"127.0.0.1:8080", "[::1]:8080"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
}

// The first target refuses, so dialFirst must move on to the second.
// Both are IPv4 so the test runs on hosts with no IPv6 loopback.
func TestDialFirst_FallsBackToNextTarget(t *testing.T) {
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddr := dead.Addr().String()
	dead.Close()

	live, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { live.Close() })

	c, err := dialFirst(context.Background(), []string{deadAddr, live.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got := c.RemoteAddr().String(); got != live.Addr().String() {
		t.Errorf("dialed %s, want %s", got, live.Addr())
	}
}
