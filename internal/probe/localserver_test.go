// SPDX-License-Identifier: MIT

package probe

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/provider"
)

// deadline bounds a probe that is expected to go unanswered, so a test does
// not sit out the full scanTimeout to learn what it already knows.
func deadline(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	t.Cleanup(cancel)
	return ctx
}

// serveJSON starts a loopback server answering path with body.
func serveJSON(t *testing.T, path, body string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, body)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// A server that answers identically on another address of this machine is
// serving more than this machine. 127.0.0.1 stands in for the non-loopback
// address here: scanOne takes the candidate list, and it is offHostAddrs that
// decides which addresses are off-host, pinned separately below.
func TestScanOne_AnAddressAnsweringTheSameServerIsReported(t *testing.T) {
	srv := serveJSON(t, "/api/version", `{"version":"9.9.9"}`)

	got := scanOne(context.Background(), srv.URL, "ollama", []string{"127.0.0.1"})
	if got.OffHost != "127.0.0.1" {
		t.Errorf("OffHost = %q, want 127.0.0.1: the same server answered there", got.OffHost)
	}
	if got.Version != "9.9.9" {
		t.Errorf("Version = %q, want 9.9.9", got.Version)
	}
	if got.Tried != 1 {
		t.Errorf("Tried = %d, want 1", got.Tried)
	}
}

// The acceptance criterion that stops a port number being treated as identity:
// something else listening on that port must not be reported as the model
// server. The handler answers differently on each request, so the second probe
// is a different server as far as the evidence goes.
func TestScanOne_ADifferentAnswerOnThatAddressIsNotThisServer(t *testing.T) {
	var n atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"version":"%d.0.0"}`, n.Add(1))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	got := scanOne(context.Background(), srv.URL, "ollama", []string{"127.0.0.1"})
	if got.OffHost != "" {
		t.Errorf("OffHost = %q, want empty: the answer there did not match this server", got.OffHost)
	}
	if got.Tried != 1 {
		t.Errorf("Tried = %d, want 1: it must still record that it looked", got.Tried)
	}
}

// Nothing listening at the candidate address is the ordinary case and must not
// be reported as exposure.
func TestScanOne_ASilentAddressIsNotExposure(t *testing.T) {
	srv := serveJSON(t, "/api/version", `{"version":"1.2.3"}`)

	// 192.0.2.0/24 is TEST-NET-1, reserved for documentation, so nothing on a
	// developer machine or a runner answers there. A dropped packet is the
	// firewall-shaped case, so it waits out a deadline; the caller's own is
	// shorter here, which is the same path at a tenth of the wall clock.
	got := scanOne(deadline(t), srv.URL, "ollama", []string{"192.0.2.1"})
	if got.OffHost != "" {
		t.Errorf("OffHost = %q, want empty", got.OffHost)
	}
	if got.Version != "1.2.3" {
		t.Errorf("Version = %q, want 1.2.3: the loopback read still has to work", got.Version)
	}
}

// An endpoint already pointing off-host is the answer, with no probe needed.
func TestScanOne_AnEndpointAlreadyOffHostNeedsNoProbe(t *testing.T) {
	got := scanOne(deadline(t), "http://192.0.2.7:11434", "ollama", nil)
	if got.OffHost != "192.0.2.7" {
		t.Errorf("OffHost = %q, want 192.0.2.7", got.OffHost)
	}
	if got.Tried != 1 {
		t.Errorf("Tried = %d, want 1", got.Tried)
	}
}

// llama.cpp publishes build_info rather than a version, and #915 already
// identifies it by that field. Reading a different one would report a server
// Hydra cannot see as unversioned.
func TestScanOne_LlamaCppVersionComesFromBuildInfo(t *testing.T) {
	srv := serveJSON(t, "/props", `{"build_info":"b4567-abc1234"}`)

	got := scanOne(context.Background(), srv.URL, "llamacpp", nil)
	if got.Version != "b4567-abc1234" {
		t.Errorf("Version = %q, want b4567-abc1234", got.Version)
	}
}

// A server publishing no version is still identifiable, and must read as
// version-unknown rather than as some other field pressed into service.
func TestScanOne_AServerWithNoVersionStillMatchesOnItsBody(t *testing.T) {
	srv := serveJSON(t, "/v1/models", `{"data":[{"id":"phi-4"}]}`)

	got := scanOne(context.Background(), srv.URL, "lmstudio", []string{"127.0.0.1"})
	if got.Version != "" {
		t.Errorf("Version = %q, want empty: LM Studio publishes none", got.Version)
	}
	if got.OffHost != "127.0.0.1" {
		t.Errorf("OffHost = %q, want 127.0.0.1: the model list identifies it", got.OffHost)
	}
}

// A server that does not answer at all leaves both unknown, and must not report
// exposure off the back of a candidate address that happens to answer.
func TestScanOne_AnUnreachableServerRulesNothingIn(t *testing.T) {
	got := scanOne(context.Background(), "http://127.0.0.1:1", "ollama", []string{"127.0.0.1"})
	if got.OffHost != "" {
		t.Errorf("OffHost = %q, want empty: there was no identity to match against", got.OffHost)
	}
	if got.Version != "" {
		t.Errorf("Version = %q, want empty", got.Version)
	}
}

func TestScanLocalServers_SkipsHeadsItCannotProbe(t *testing.T) {
	srv := serveJSON(t, "/api/version", `{"version":"0.33.2"}`)
	heads := []provider.Head{
		{ID: "ollama/qwen3:0.6b", Source: "port", Endpoint: srv.URL},
		{ID: "ollama/qwen2.5:7b", Source: "port", Endpoint: srv.URL}, // same server, one entry
		{ID: "claude", Source: "cli"},                                // not a local server
		{ID: "vllm/mistral", Source: "port", Endpoint: srv.URL},      // kind with no identity probe
		{ID: "ollama/no-endpoint", Source: "port"},                   // nothing to dial
	}

	got := ScanLocalServers(context.Background(), heads)
	if len(got) != 1 {
		t.Fatalf("got %d server(s), want 1 (two heads share one endpoint): %+v", len(got), got)
	}
	if got[0].Kind != "ollama" || got[0].Version != "0.33.2" {
		t.Errorf("got %+v, want kind ollama version 0.33.2", got[0])
	}
}

func TestScanLocalServers_NoLocalHeadsIsNoScan(t *testing.T) {
	if got := ScanLocalServers(context.Background(), []provider.Head{{ID: "claude", Source: "cli"}}); got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}

// offHostAddrs is what decides which addresses count as off this machine, so
// the whole check rests on it never returning one that is loopback.
func TestOffHostAddrs_NeverReturnsALoopbackAddress(t *testing.T) {
	for _, a := range offHostAddrs() {
		ip := net.ParseIP(a)
		if ip == nil {
			t.Errorf("%q is not an IP", a)
			continue
		}
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
			t.Errorf("offHostAddrs returned %s, which is not an address other machines reach this one by", a)
		}
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for _, in := range []string{"localhost", "LocalHost", "127.0.0.1", "127.0.0.53", "::1"} {
		if !isLoopbackHost(in) {
			t.Errorf("isLoopbackHost(%q) = false", in)
		}
	}
	for _, in := range []string{"192.168.1.10", "0.0.0.0", "example.internal", ""} {
		if isLoopbackHost(in) {
			t.Errorf("isLoopbackHost(%q) = true", in)
		}
	}
}

// A body larger than the read cap must not be treated as a match by truncation
// alone: two different large bodies would both truncate to the same prefix.
func TestIdentify_ReadsEnoughToTellTwoServersApart(t *testing.T) {
	long := strings.Repeat("a", 4096)
	srv := serveJSON(t, "/v1/models", `{"data":"`+long+`x"}`)

	id, _ := identify(context.Background(), srv.URL, "lmstudio")
	if !strings.HasSuffix(id, `x"}`) {
		t.Errorf("identity ends %q, want the whole body", id[max(0, len(id)-8):])
	}
}
