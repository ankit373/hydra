// SPDX-License-Identifier: MIT

package port

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/capabilities"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

func caps(t *testing.T) *capabilities.DB {
	t.Helper()
	db, err := capabilities.Load("")
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// The bug: the executor honoured $OLLAMA_HOST while this package hardcoded
// localhost:11434, so a user running Ollama anywhere else had a working server
// discovery could not see, no tier-10 head, and a silent degrade to a paid
// one (#282). A stub server on an arbitrary port is exactly that situation.
func TestOllama_DiscoversModelsOnANonDefaultAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"qwen3:8b"},{"name":"llama3.2:3b"}]}`))
	}))
	defer srv.Close()

	svc := &ollamaService{base: srv.URL}
	heads, err := svc.probe(context.Background(), caps(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(heads) != 2 {
		t.Fatalf("got %d heads, want 2: %+v", len(heads), heads)
	}

	for _, h := range heads {
		if !strings.HasPrefix(h.ID, "ollama/") {
			t.Errorf("ID = %q, want an ollama/ prefix", h.ID)
		}
		if !h.LocalOnly {
			t.Errorf("%s: LocalOnly = false, it would lose tier 10 and could be routed to as paid", h.ID)
		}
		if h.Source != "port" {
			t.Errorf("%s: Source = %q, want %q", h.ID, h.Source, "port")
		}
		// The address it was found at, not a constant. The executor dials this
		// later, so a head found at one address and stamped with another fails
		// at the point of use.
		if h.Endpoint != srv.URL {
			t.Errorf("%s: Endpoint = %q, want %q, the address it was actually discovered at",
				h.ID, h.Endpoint, srv.URL)
		}
	}
}

func TestLMStudio_DiscoversModelsOnANonDefaultAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"some-local-model"}]}`))
	}))
	defer srv.Close()

	svc := &lmStudioService{base: srv.URL}
	heads, err := svc.probe(context.Background(), caps(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(heads) != 1 {
		t.Fatalf("got %d heads, want 1: %+v", len(heads), heads)
	}
	if !heads[0].LocalOnly {
		t.Error("LocalOnly = false for an LM Studio head")
	}
	if heads[0].Endpoint != srv.URL {
		t.Errorf("Endpoint = %q, want %q", heads[0].Endpoint, srv.URL)
	}
}

// Embedding-only models report capabilities but never "completion", so they
// failed every dispatch (#532). They are now discovered but MARKED, so
// executor.Unroutable keeps them out of routing while surfaces can still show
// them; only the positively-non-completion case is marked, no capabilities
// field at all (older servers) stays routable.
func TestOllama_MarksEmbeddingOnlyModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"models":[
			{"name":"qwen3:0.6b","capabilities":["completion"]},
			{"name":"nomic-embed-text:latest","capabilities":["embedding"]},
			{"name":"llama3.2:3b"},
			{"name":"qwen2.5-coder:7b","capabilities":["completion","tools","insert"]}
		]}`))
	}))
	defer srv.Close()

	heads, err := (&ollamaService{base: srv.URL}).probe(context.Background(), caps(t))
	if err != nil {
		t.Fatal(err)
	}

	ids := make(map[string]bool, len(heads))
	for _, h := range heads {
		ids[h.ID] = true
	}

	for _, want := range []string{"ollama/qwen3:0.6b", "ollama/llama3.2:3b", "ollama/qwen2.5-coder:7b", "ollama/nomic-embed-text:latest"} {
		if !ids[want] {
			t.Errorf("missing expected head %q among %+v", want, ids)
		}
	}
	if len(heads) != 4 {
		t.Errorf("got %d heads, want 4 (embedding-only included but marked): %+v", len(heads), heads)
	}
	for _, h := range heads {
		marked := h.Meta["embedding_only"] == "true"
		if h.ID == "ollama/nomic-embed-text:latest" {
			if !marked {
				t.Error("the embedding-only model is not marked, dispatch would try it and fail (#532)")
			}
			if executor.Supports(h) {
				t.Error("an embedding-only model must never be a dispatch candidate (#532)")
			}
			continue
		}
		if marked {
			t.Errorf("%s is completion-capable but marked embedding-only", h.ID)
		}
		if !executor.Supports(h) {
			t.Errorf("%s became unroutable: %s", h.ID, executor.Unroutable(h))
		}
	}
}

// A server that answers but not with what we asked for is not a head. Returning
// one anyway is the #248 failure: advertising something dispatch cannot use.
func TestProbe_NonOKStatusYieldsNoHeads(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := (&ollamaService{base: srv.URL}).probe(context.Background(), caps(t)); err == nil {
		t.Error("a 500 from the Ollama probe produced no error")
	}
	if _, err := (&lmStudioService{base: srv.URL}).probe(context.Background(), caps(t)); err == nil {
		t.Error("a 500 from the LM Studio probe produced no error")
	}
}

func TestProbe_GarbageBodyYieldsNoHeads(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("this is not json"))
	}))
	defer srv.Close()

	if _, err := (&ollamaService{base: srv.URL}).probe(context.Background(), caps(t)); err == nil {
		t.Error("an unparsable body produced no error")
	}
}

// An empty model list means the server is up with nothing loaded. That is zero
// heads and no error, distinct from a failed probe.
func TestOllama_NoModelsIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer srv.Close()

	heads, err := (&ollamaService{base: srv.URL}).probe(context.Background(), caps(t))
	if err != nil {
		t.Fatalf("an empty model list should not be an error: %v", err)
	}
	if len(heads) != 0 {
		t.Errorf("got %d heads from an empty list", len(heads))
	}
}

// The liveness dial and the HTTP probe must target the same place. Dialling the
// wrong address answers a different question than the probe that follows it.
func TestHostPort_TracksTheBaseURL(t *testing.T) {
	cases := []struct {
		base, want  string
		defaultPort int
	}{
		{"http://localhost:11434", "localhost:11434", 11434},
		{"http://127.0.0.1:11500", "127.0.0.1:11500", 11434},
		{"http://localhost", "localhost:11434", 11434},
		{"https://ollama.internal", "ollama.internal:11434", 11434},
		{"http://localhost:1234", "localhost:1234", 1234},
		{"", "localhost:11434", 11434},
		{"::::not a url", "localhost:11434", 11434},
	}
	for _, tc := range cases {
		if got := hostPort(tc.base, tc.defaultPort); got != tc.want {
			t.Errorf("hostPort(%q, %d) = %q, want %q", tc.base, tc.defaultPort, got, tc.want)
		}
	}
}

// The service's dial address must be derived from the base it will probe,
// checked directly, because these being independently computed is what allowed
// them to disagree in the first place.
func TestOllamaService_AddrMatchesItsBase(t *testing.T) {
	for _, base := range []string{
		"http://localhost:11434",
		"http://127.0.0.1:11500",
		"https://ollama.internal:8443",
	} {
		svc := &ollamaService{base: base}
		if got, want := svc.addr(), hostPort(base, 11434); got != want {
			t.Errorf("addr() = %q for base %q, want %q", got, base, want)
		}
		if !strings.Contains(base, svc.addr()) && !strings.HasPrefix(svc.addr(), "localhost") {
			t.Errorf("addr() %q is not the host of base %q", svc.addr(), base)
		}
	}
}

// Discovery and execution must resolve $OLLAMA_HOST to the same address. They
// did not, that is #282, and one shared resolver is what stops them drifting
// again. Asserted through the sandbox so no developer's own OLLAMA_HOST leaks in.
func TestDiscovery_ResolvesTheSameHostTheExecutorWill(t *testing.T) {
	cases := []struct{ env, want string }{
		{"", provider.DefaultOllamaHost},
		{"http://127.0.0.1:11500", "http://127.0.0.1:11500"},
		{"127.0.0.1:11500", "http://127.0.0.1:11500"},          // bare host:port, as Ollama accepts
		{"https://ollama.internal", "https://ollama.internal"}, // https to a remote host is allowed
		// Plain http to a remote host is refused: a prompt is the user's source
		// code, and shipping it in cleartext because an env var said so is not a
		// tradeoff to make silently.
		{"http://192.168.1.50:11434", provider.DefaultOllamaHost},
		{"not a url at all", provider.DefaultOllamaHost},
		{"ftp://localhost:11434", provider.DefaultOllamaHost},
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			s := testutil.NewSandbox(t)
			// Explicit even for "", rather than relying on the sandbox's own
			// baseline: NewSandbox points OLLAMA_HOST at a dead address by
			// default so an unrelated test never discovers a real local
			// Ollama server (#539), so "no override" has to be asserted here,
			// not borrowed from whatever the sandbox happens to leave behind.
			s.SetKey(t, "OLLAMA_HOST", tc.env)
			got := provider.OllamaHost()
			if got != tc.want {
				t.Errorf("OllamaHost() = %q with OLLAMA_HOST=%q, want %q", got, tc.env, tc.want)
			}
			// And the service built from it agrees.
			svc := &ollamaService{base: got}
			if !strings.HasPrefix(svc.base, got) {
				t.Errorf("service base %q does not match resolved host %q", svc.base, got)
			}
		})
	}
}

// Discover walks every service. With nothing listening it must find nothing and
// not hang, a probe that blocks is worse than one that finds nothing, because
// `hyctl probe` is often the first command a user runs.
func TestDiscover_NothingListeningFindsNothingQuickly(t *testing.T) {
	// The sandbox points every relocatable service at a port nothing is on, so
	// the liveness dial fails fast and a machine that really runs one does not
	// fail a test about finding nothing. LM Studio was the exception until it
	// was given a variable to be relocated by (#988).
	testutil.NewSandbox(t)

	start := time.Now()
	heads, err := (&Provider{}).Discover(context.Background())
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Discover errored with nothing listening: %v", err)
	}
	if len(heads) != 0 {
		t.Errorf("found %d heads with nothing listening: %+v", len(heads), heads)
	}
	// Every service dials concurrently with a 400ms timeout, plus slack.
	if elapsed > 5*time.Second {
		t.Errorf("Discover took %v with nothing listening", elapsed)
	}
}

// isOpen is the cheap liveness check that saves paying the HTTP timeout per
// service. It must be accurate in both directions or Discover either skips a
// live server or waits out a dead one.
func TestIsOpen_DetectsListeningAndNot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "http://")
	if !isOpen(addr) {
		t.Errorf("isOpen(%q) = false for a listening server", addr)
	}
	if isOpen("127.0.0.1:1") {
		t.Error("isOpen reported a closed port as open")
	}
	if isOpen("not-a-valid-address") {
		t.Error("isOpen reported a malformed address as open")
	}
}

// Discover finds a real service when one is listening, the positive case that
// proves the negative ones above are not passing for the wrong reason.
func TestDiscover_FindsAListeningOllama(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"qwen3:8b"}]}`))
	}))
	defer srv.Close()

	s := testutil.NewSandbox(t)
	s.SetKey(t, "OLLAMA_HOST", srv.URL)

	heads, err := (&Provider{}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The stubbed head, not the whole list: Discover dials this machine's real
	// ports as well, so asserting a count here fails on a developer running LM
	// Studio or a LiteLLM proxy, for reasons that have nothing to do with
	// Ollama. The fan-out tests assert the exact set, against stub services.
	var found *provider.Head
	for i := range heads {
		if heads[i].ID == "ollama/qwen3:8b" {
			found = &heads[i]
		}
	}
	if found == nil {
		t.Fatalf("the served model was not discovered, got %+v", heads)
	}
	if found.Endpoint != srv.URL {
		t.Errorf("Endpoint = %q, want the address it was found at", found.Endpoint)
	}
	if got := found.Meta["model_source"]; got != "builtin" {
		t.Errorf("Meta[model_source] = %q, want builtin, qwen matches a curated family pattern", got)
	}
}

// Each service's dial address is derived from the base it will probe, for both
// services, checked directly, since computing them independently is what let
// them disagree before.
func TestServiceAddrs_DeriveFromTheirBases(t *testing.T) {
	if got, want := (&ollamaService{base: "http://127.0.0.1:11500"}).addr(), "127.0.0.1:11500"; got != want {
		t.Errorf("ollama addr = %q, want %q", got, want)
	}
	if got, want := (&lmStudioService{base: "http://127.0.0.1:4321"}).addr(), "127.0.0.1:4321"; got != want {
		t.Errorf("lmstudio addr = %q, want %q", got, want)
	}
	// Default ports are supplied when the base carries none.
	if got := (&ollamaService{base: "http://localhost"}).addr(); got != "localhost:11434" {
		t.Errorf("ollama addr with no port = %q, want localhost:11434", got)
	}
	if got := (&lmStudioService{base: "http://localhost"}).addr(); got != "localhost:1234" {
		t.Errorf("lmstudio addr with no port = %q, want localhost:1234", got)
	}
}

// Each probe reads a different vendor's response shape. A non-200, an
// unparsable body or an empty catalogue must each yield no heads rather than a
// head Hydra cannot drive, discovery reporting something that is not there is
// the #248 defect class.
func TestProbes_RejectBadResponses(t *testing.T) {
	caps, err := capabilities.Load("")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"server error", http.StatusInternalServerError, ``},
		{"not found", http.StatusNotFound, `{}`},
		{"unparsable body", http.StatusOK, `{truncated`},
		{"empty catalogue", http.StatusOK, `{"models":[],"data":[]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			for _, svc := range []portService{
				&ollamaService{base: srv.URL},
				&lmStudioService{base: srv.URL},
			} {
				heads, err := svc.probe(context.Background(), caps)
				if len(heads) != 0 {
					t.Errorf("%T returned %d heads for a %s response: %+v",
						svc, len(heads), tt.name, heads)
				}
				if tt.status != http.StatusOK && err == nil {
					t.Errorf("%T reported success on HTTP %d", svc, tt.status)
				}
			}
		})
	}

	// Nothing listening at all: an error, not a silent empty success that
	// looks the same as "the server is up and has no models".
	for _, svc := range []portService{
		&ollamaService{base: "http://127.0.0.1:1"},
		&lmStudioService{base: "http://127.0.0.1:1"},
	} {
		if heads, err := svc.probe(context.Background(), caps); err == nil {
			t.Errorf("%T reported success against a dead port: %+v", svc, heads)
		}
	}
}

// LM Studio speaks the OpenAI catalogue shape, Ollama its own. Each head must
// carry the address it was actually found at, the executor dials it later, so
// a head discovered at one address and stamped with another fails at the point
// of use (#282).
func TestProbes_StampTheAddressTheyWereFoundAt(t *testing.T) {
	caps, err := capabilities.Load("")
	if err != nil {
		t.Fatal(err)
	}

	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"name":"qwen2.5-coder:7b"},{"name":"llama3.2:3b"}]}`))
	}))
	defer ollama.Close()

	heads, err := (&ollamaService{base: ollama.URL}).probe(context.Background(), caps)
	if err != nil {
		t.Fatal(err)
	}
	if len(heads) != 2 {
		t.Fatalf("got %d heads, want one per model", len(heads))
	}
	for _, h := range heads {
		if h.Endpoint != ollama.URL {
			t.Errorf("%s is stamped with %q but was found at %q; the executor "+
				"dials the stamp (#282)", h.ID, h.Endpoint, ollama.URL)
		}
		if !h.LocalOnly {
			t.Errorf("%s is not marked local; it would not sit at the terminal tier", h.ID)
		}
		if h.CapScore <= 0 {
			t.Errorf("%s has no capability score, so it cannot be ranked", h.ID)
		}
	}

	lms := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"mistral-7b-instruct"}]}`))
	}))
	defer lms.Close()

	lmHeads, err := (&lmStudioService{base: lms.URL}).probe(context.Background(), caps)
	if err != nil {
		t.Fatal(err)
	}
	if len(lmHeads) != 1 {
		t.Fatalf("got %d LM Studio heads, want 1", len(lmHeads))
	}
	if lmHeads[0].Endpoint != lms.URL {
		t.Errorf("LM Studio head stamped with %q, found at %q", lmHeads[0].Endpoint, lms.URL)
	}
	// The two services must not collide in the head-id namespace.
	if lmHeads[0].ID == heads[0].ID {
		t.Error("an LM Studio head and an Ollama head share an id")
	}
}

// The isolation, proven against the real port rather than asserted. Every other
// test here says "nothing is listening", which is a claim about the machine, so
// none of them could tell that discovery was reaching the developer's own
// LM Studio: a stub left on 1234 by a concurrent session turned five tests red
// across three packages (#988).
//
// The server is real and on the documented port. If something else already
// holds it, that is the same condition under test and the assertion still
// stands, so there is nothing to skip.
func TestDiscover_IgnoresAServerOnLMStudiosRealPort(t *testing.T) {
	testutil.NewSandbox(t)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"a-model-nobody-asked-for","object":"model"}]}`))
	}))
	if ln, err := net.Listen("tcp", "127.0.0.1:1234"); err == nil {
		srv.Listener.Close()
		srv.Listener = ln
		srv.Start()
		defer srv.Close()
	}

	heads, err := (&Provider{}).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover errored: %v", err)
	}
	for _, h := range heads {
		if strings.HasPrefix(h.ID, "lmstudio/") {
			t.Errorf("discovery reached a server on the real port: %s at %s", h.ID, h.Endpoint)
		}
	}
}

// The other half of the same change: the variable exists so a real LM Studio on
// a port other than 1234 is discoverable at all. Its port is configurable in
// the app, and before this Hydra could only ever look at the default, the #282
// shape one service later.
func TestDiscover_HonoursTheLMStudioHostVariable(t *testing.T) {
	testutil.NewSandbox(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"relocated-model","object":"model"}]}`))
	}))
	defer srv.Close()
	t.Setenv(LMStudioHostEnv, srv.URL)

	heads, err := (&Provider{}).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover errored: %v", err)
	}
	for _, h := range heads {
		if h.ID == "lmstudio/relocated-model" {
			if h.Endpoint != srv.URL {
				t.Errorf("Endpoint = %q, want the relocated %q", h.Endpoint, srv.URL)
			}
			return
		}
	}
	t.Errorf("a relocated LM Studio was not discovered: %+v", heads)
}
