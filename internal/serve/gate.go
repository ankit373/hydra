// SPDX-License-Identifier: MIT

package serve

import (
	"math"
	"mime"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Default limits for one server process. A 429 is in every OpenAI SDK's retry
// class, so a client over them backs off and retries rather than failing.
const (
	DefaultMaxInFlight = 8
	DefaultPerMinute   = 120
)

// Option changes what Serve accepts.
type Option func(*gate)

// AllowHost accepts addr's host as a Host header besides the bound address,
// for an --addr given by name. A wildcard bind already accepts any Host.
func AllowHost(addr string) Option {
	return func(g *gate) {
		if g.hosts == nil {
			return
		}
		if host, port, err := net.SplitHostPort(strings.TrimSpace(addr)); err == nil && host != "" {
			g.hosts = append(g.hosts, hostKey(host, port))
		}
	}
}

// AllowOrigins accepts requests carrying one of these Origin values.
func AllowOrigins(origins ...string) Option {
	return func(g *gate) {
		for _, o := range origins {
			if o = strings.TrimRight(strings.TrimSpace(o), "/"); o != "" {
				g.origins = append(g.origins, strings.ToLower(o))
			}
		}
	}
}

// Limit caps requests that dispatch: at most maxInFlight at once and perMinute
// in any one minute. Zero turns that cap off.
func Limit(maxInFlight, perMinute int) Option {
	return func(g *gate) { g.maxInFlight, g.perMinute = maxInFlight, perMinute }
}

// gate is what every request passes before the handler sees it.
type gate struct {
	next        http.Handler
	hosts       []string // nil accepts any Host: a wildcard bind, which needs a token
	origins     []string
	maxInFlight int
	perMinute   int

	slots chan struct{}
	now   func() time.Time

	mu          sync.Mutex
	windowStart time.Time
	windowCount int
}

func newGate(next http.Handler, bound net.Addr, opts ...Option) *gate {
	g := &gate{next: next, maxInFlight: DefaultMaxInFlight, perMinute: DefaultPerMinute, now: time.Now}
	g.hosts = boundHosts(bound)
	for _, o := range opts {
		o(g)
	}
	if g.maxInFlight > 0 {
		g.slots = make(chan struct{}, g.maxInFlight)
	}
	return g
}

// boundHosts is the Host values a request to this listener may carry. A
// loopback bind answers to every loopback name; a wildcard bind to any.
func boundHosts(a net.Addr) []string {
	tcp, ok := a.(*net.TCPAddr)
	if !ok || tcp.IP.IsUnspecified() {
		return nil
	}
	port := strconv.Itoa(tcp.Port)
	if tcp.IP.IsLoopback() {
		return []string{hostKey("localhost", port), hostKey("127.0.0.1", port), hostKey("::1", port)}
	}
	return []string{hostKey(tcp.IP.String(), port)}
}

func hostKey(host, port string) string {
	return net.JoinHostPort(strings.ToLower(strings.Trim(host, "[]")), port)
}

func (g *gate) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if g.hosts != nil && !slices.Contains(g.hosts, requestHost(req.Host)) {
		writeError(w, http.StatusForbidden, "Host "+strconv.Quote(req.Host)+" is not the address this server is bound to")
		return
	}
	if origin := req.Header.Get("Origin"); origin != "" {
		if !slices.Contains(g.origins, strings.ToLower(strings.TrimRight(origin, "/"))) {
			writeError(w, http.StatusForbidden, "Origin "+strconv.Quote(origin)+" is not allowed; see --allow-origin")
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Add("Vary", "Origin")
		if req.Method == http.MethodOptions {
			preflight(w, req)
			return
		}
	}
	if req.Method != http.MethodPost {
		g.next.ServeHTTP(w, req)
		return
	}
	if mt, _, err := mime.ParseMediaType(req.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "send Content-Type: application/json")
		return
	}
	if wait, ok := g.admit(); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(wait))
		writeError(w, http.StatusTooManyRequests, "too many requests to this server; retry shortly")
		return
	}
	if g.slots != nil {
		defer func() { <-g.slots }()
	}
	g.next.ServeHTTP(w, req)
}

// admit takes an in-flight slot and a place in this minute's count, or says
// how many seconds to wait.
func (g *gate) admit() (int, bool) {
	if g.slots != nil {
		select {
		case g.slots <- struct{}{}:
		default:
			return 1, false
		}
	}
	if g.perMinute <= 0 {
		return 0, true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if now.Sub(g.windowStart) >= time.Minute {
		g.windowStart, g.windowCount = now, 0
	}
	if g.windowCount >= g.perMinute {
		if g.slots != nil {
			<-g.slots
		}
		return int(math.Ceil((time.Minute - now.Sub(g.windowStart)).Seconds())), false
	}
	g.windowCount++
	return 0, true
}

// requestHost normalises a Host header; one with no port is on port 80.
func requestHost(h string) string {
	host, port, err := net.SplitHostPort(h)
	if err != nil {
		host, port = h, "80"
	}
	return hostKey(host, port)
}

// preflight answers a browser's CORS check for an allowed origin.
func preflight(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST")
	if h := req.Header.Get("Access-Control-Request-Headers"); h != "" {
		w.Header().Set("Access-Control-Allow-Headers", h)
	}
	w.WriteHeader(http.StatusNoContent)
}
