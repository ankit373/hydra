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
	"testing"
	"time"
)

func TestAllowed_Literal(t *testing.T) {
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
		{"ipv6 loopback", "::1", true},
		{"ipv6 unique-local", "fc00::1", true},
		{"ipv6 public", "2001:4860:4860::8888", false},
		{"ipv4 loopback with port", "127.0.0.1:9999", true},
		{"ipv4 public with port", "8.8.8.8:443", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			allowed, err := Allowed(context.Background(), tc.host, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if allowed != tc.allowed {
				t.Errorf("Allowed(%q) = %v, want %v", tc.host, allowed, tc.allowed)
			}
		})
	}
}

func TestAllowed_Resolved(t *testing.T) {
	priv := net.ParseIP("10.0.0.1")
	pub := net.ParseIP("8.8.8.8")

	t.Run("all private allows", func(t *testing.T) {
		resolve := func(context.Context, string) ([]net.IP, error) { return []net.IP{priv}, nil }
		allowed, err := Allowed(context.Background(), "ollama.local", resolve)
		if err != nil || !allowed {
			t.Errorf("got (%v, %v), want (true, nil)", allowed, err)
		}
	})

	t.Run("one public denies", func(t *testing.T) {
		resolve := func(context.Context, string) ([]net.IP, error) { return []net.IP{priv, pub}, nil }
		allowed, err := Allowed(context.Background(), "mixed.local", resolve)
		if err != nil || allowed {
			t.Errorf("got (%v, %v), want (false, nil)", allowed, err)
		}
	})

	t.Run("resolve error fails closed", func(t *testing.T) {
		wantErr := errors.New("no such host")
		resolve := func(context.Context, string) ([]net.IP, error) { return nil, wantErr }
		allowed, err := Allowed(context.Background(), "broken.local", resolve)
		if allowed || !errors.Is(err, wantErr) {
			t.Errorf("got (%v, %v), want (false, %v)", allowed, err, wantErr)
		}
	})

	t.Run("no addresses fails closed", func(t *testing.T) {
		resolve := func(context.Context, string) ([]net.IP, error) { return nil, nil }
		allowed, err := Allowed(context.Background(), "empty.local", resolve)
		if allowed || err == nil {
			t.Errorf("got (%v, %v), want (false, non-nil)", allowed, err)
		}
	})
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
