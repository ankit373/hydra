// SPDX-License-Identifier: MIT

package serve

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const chatBody = `{"model":"hydra","messages":[{"role":"user","content":"q"}]}`

// serveOn runs Serve on a loopback port and returns its base URL.
func serveOn(t *testing.T, r Router, opts ...Option) string {
	t.Helper()
	ln, err := Listen("127.0.0.1:0", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ln, Handler(r, nil, ""), opts...) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	return "http://" + ln.Addr().String()
}

// send makes one request; host, when set, replaces the Host header.
func send(t *testing.T, method, url, host, body string, header map[string]string) int {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	return res.StatusCode
}

var jsonType = map[string]string{"Content-Type": "application/json"}

func TestServe_RefusesAHostItIsNotBoundTo(t *testing.T) {
	r := &stubRouter{}
	base := serveOn(t, r)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(base, "http://"))

	for _, host := range []string{"attacker.example:" + port, "attacker.example", "127.0.0.1:1"} {
		if got := send(t, http.MethodGet, base+"/v1/models", host, "", nil); got != http.StatusForbidden {
			t.Errorf("GET /v1/models with Host %q: status %d, want 403", host, got)
		}
		if got := send(t, http.MethodPost, base+"/v1/chat/completions", host, chatBody, jsonType); got != http.StatusForbidden {
			t.Errorf("POST with Host %q: status %d, want 403", host, got)
		}
	}
	if r.got.RequestID != "" {
		t.Error("a request with a foreign Host reached the router")
	}
	for _, host := range []string{"", "localhost:" + port, "[::1]:" + port, "LOCALHOST:" + port} {
		if got := send(t, http.MethodGet, base+"/v1/models", host, "", nil); got != http.StatusOK {
			t.Errorf("GET /v1/models with Host %q: status %d, want 200", host, got)
		}
	}
}

func TestServe_RefusesAnOriginNotAllowed(t *testing.T) {
	r := &stubRouter{answer: Answer{Output: "hi"}}
	base := serveOn(t, r)

	foreign := map[string]string{"Content-Type": "application/json", "Origin": "https://attacker.example"}
	if got := send(t, http.MethodPost, base+"/v1/chat/completions", "", chatBody, foreign); got != http.StatusForbidden {
		t.Errorf("POST with a foreign Origin: status %d, want 403", got)
	}
	if got := send(t, http.MethodGet, base+"/v1/models", "", "", foreign); got != http.StatusForbidden {
		t.Errorf("GET with a foreign Origin: status %d, want 403", got)
	}
	if r.got.RequestID != "" {
		t.Error("a request with a foreign Origin reached the router")
	}
}

func TestServe_RefusesAPostThatIsNotJSON(t *testing.T) {
	r := &stubRouter{answer: Answer{Output: "hi"}}
	base := serveOn(t, r)

	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x"} {
		got := send(t, http.MethodPost, base+"/v1/chat/completions", "", chatBody, map[string]string{"Content-Type": ct})
		if got != http.StatusUnsupportedMediaType {
			t.Errorf("POST with Content-Type %q: status %d, want 415", ct, got)
		}
	}
	if r.got.RequestID != "" {
		t.Error("a POST that was not JSON reached the router")
	}
	withCharset := map[string]string{"Content-Type": "application/json; charset=utf-8"}
	if got := send(t, http.MethodPost, base+"/v1/chat/completions", "", chatBody, withCharset); got != http.StatusOK {
		t.Errorf("POST with JSON and a charset: status %d, want 200", got)
	}
}

func TestServe_AnswersAnAllowedOriginWithCORS(t *testing.T) {
	base := serveOn(t, &stubRouter{answer: Answer{Output: "hi"}}, AllowOrigins("http://localhost:3000/"))

	req, _ := http.NewRequest(http.MethodOptions, base+"/v1/chat/completions", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNoContent || res.Header.Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Errorf("preflight: status %d, allow-origin %q", res.StatusCode, res.Header.Get("Access-Control-Allow-Origin"))
	}
	allowed := map[string]string{"Content-Type": "application/json", "Origin": "http://localhost:3000"}
	if got := send(t, http.MethodPost, base+"/v1/chat/completions", "", chatBody, allowed); got != http.StatusOK {
		t.Errorf("POST from an allowed origin: status %d, want 200", got)
	}
}

// A name given as --addr is a Host the operator meant to be reached by.
func TestServe_AcceptsTheConfiguredAddress(t *testing.T) {
	base := serveOn(t, &stubRouter{}, AllowHost("hydra.internal:8787"))
	if got := send(t, http.MethodGet, base+"/v1/models", "hydra.internal:8787", "", nil); got != http.StatusOK {
		t.Errorf("Host from --addr: status %d, want 200", got)
	}
}

// --addr naming a wildcard must not narrow a wildcard bind to that literal.
func TestGate_AWildcardAddrStillAcceptsAnyHost(t *testing.T) {
	g := newGate(Handler(&stubRouter{}, nil, ""), &net.TCPAddr{IP: net.IPv4zero, Port: 8788}, AllowHost("0.0.0.0:8788"))
	req := httptest.NewRequest(http.MethodGet, "http://myhost.lan:8788/v1/models", nil)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("Host myhost.lan on a wildcard bind: status %d, want 200", w.Code)
	}
}

// A wildcard bind cannot know its own names, and needs a token anyway.
func TestBoundHosts_AWildcardBindAcceptsAnyHost(t *testing.T) {
	if got := boundHosts(&net.TCPAddr{IP: net.IPv4zero, Port: 8788}); got != nil {
		t.Errorf("wildcard bind accepts only %v", got)
	}
	got := boundHosts(&net.TCPAddr{IP: net.ParseIP("192.168.1.5"), Port: 8788})
	if len(got) != 1 || got[0] != "192.168.1.5:8788" {
		t.Errorf("specific bind accepts %v", got)
	}
}

// blockingRouter holds every request until release is closed.
type blockingRouter struct {
	entered chan struct{}
	release chan struct{}
}

func (b *blockingRouter) Chat(context.Context, Request) (Answer, error) {
	b.entered <- struct{}{}
	<-b.release
	return Answer{Output: "hi"}, nil
}

func (b *blockingRouter) Models() []Model { return nil }

func postJSON(t *testing.T, h http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/v1/chat/completions", strings.NewReader(chatBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

var loopbackAddr = &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8787}

func TestGate_RefusesOverTheInFlightCap(t *testing.T) {
	b := &blockingRouter{entered: make(chan struct{}), release: make(chan struct{})}
	g := newGate(Handler(b, nil, ""), loopbackAddr, Limit(1, 0))

	first := make(chan int)
	go func() { first <- postJSON(t, g).Code }()
	<-b.entered

	w := postJSON(t, g)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Errorf("second request while one is in flight: status %d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
	close(b.release)
	if code := <-first; code != http.StatusOK {
		t.Errorf("first request: status %d", code)
	}
	go func() { <-b.entered }()
	if code := postJSON(t, g).Code; code != http.StatusOK {
		t.Errorf("after the first finished: status %d, want the slot back", code)
	}
}

func TestGate_RefusesOverThePerMinuteCap(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	g := newGate(Handler(&stubRouter{}, nil, ""), loopbackAddr, Limit(0, 2))
	g.now = func() time.Time { return now }

	for i := range 2 {
		if code := postJSON(t, g).Code; code != http.StatusOK {
			t.Fatalf("request %d: status %d", i+1, code)
		}
	}
	w := postJSON(t, g)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "60" {
		t.Errorf("third request in the minute: status %d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
	now = now.Add(time.Minute)
	if code := postJSON(t, g).Code; code != http.StatusOK {
		t.Errorf("next minute: status %d, want 200", code)
	}
}
