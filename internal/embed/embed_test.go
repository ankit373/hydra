// SPDX-License-Identifier: MIT

package embed

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/provider"
)

// fakeEmbedder records what it was asked to embed.
type fakeEmbedder struct {
	mu   sync.Mutex
	seen []string
	vec  []float32
	err  error
	gate chan struct{} // when non-nil, Embed waits on it
}

func (f *fakeEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	f.seen = append(f.seen, text)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	v := f.vec
	if v == nil {
		v = []float32{1, 0, 0, 0}
	}
	return v, nil
}
func (f *fakeEmbedder) Available() bool { return true }
func (f *fakeEmbedder) Model() string   { return "fake" }
func (f *fakeEmbedder) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

func spanID(n int) string { return fmt.Sprintf("%016x", n) }

// --- Resolve ---

func TestResolve_NoEmbeddingModelIsUnavailableNotAnError(t *testing.T) {
	heads := []provider.Head{
		{ID: "ollama/llama3", Endpoint: "http://x", Meta: map[string]string{}},
	}
	e := Resolve(heads, "")
	if e.Available() {
		t.Fatal("a machine with no embedding model must report unavailable")
	}
	if _, err := e.Embed(context.Background(), "hi"); err != ErrUnavailable {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

func TestResolve_PicksTheHeadTheServerCalledEmbeddingOnly(t *testing.T) {
	heads := []provider.Head{
		{ID: "ollama/llama3", Endpoint: "http://x", Meta: map[string]string{}},
		{ID: "ollama/nomic-embed-text", Endpoint: "http://y",
			Meta: map[string]string{"embedding_only": "true"}},
	}
	e := Resolve(heads, "")
	if !e.Available() || e.Model() != "nomic-embed-text" {
		t.Fatalf("want nomic-embed-text, got available=%v model=%q", e.Available(), e.Model())
	}
}

// A model name alone must never make a head an embedder: the server never said
// so, and guessing is how a capability gets fabricated.
func TestResolve_DoesNotGuessFromTheModelName(t *testing.T) {
	heads := []provider.Head{
		{ID: "ollama/nomic-embed-text", Endpoint: "http://y", Meta: map[string]string{}},
	}
	if Resolve(heads, "").Available() {
		t.Fatal("an unmarked head must not be adopted on the strength of its name")
	}
}

func TestResolve_ConfiguredNameWins(t *testing.T) {
	heads := []provider.Head{
		{ID: "ollama/nomic-embed-text", Endpoint: "http://y",
			Meta: map[string]string{"embedding_only": "true"}},
		{ID: "ollama/mxbai-embed-large", Endpoint: "http://y",
			Meta: map[string]string{"embedding_only": "true"}},
	}
	if got := Resolve(heads, "mxbai-embed-large").Model(); got != "mxbai-embed-large" {
		t.Fatalf("configured model ignored, got %q", got)
	}
}

// --- the two Ollama dialects ---

func embedServer(t *testing.T, serve map[string]func(w http.ResponseWriter)) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fn, ok := serve[r.URL.Path]; ok {
			fn(w)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestEmbed_ModernRoute(t *testing.T) {
	s := embedServer(t, map[string]func(http.ResponseWriter){
		"/api/embed": func(w http.ResponseWriter) {
			json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{{1, 2, 3}}})
		},
	})
	v, err := newOllama(s.URL, "m").Embed(context.Background(), "hello")
	if err != nil || len(v) != 3 || v[2] != 3 {
		t.Fatalf("got %v, %v", v, err)
	}
}

// An older server serves only /api/embeddings. Which route exists is a property
// of the install, so it must be discovered rather than assumed.
func TestEmbed_FallsBackToLegacyRoute(t *testing.T) {
	var hits int
	s := embedServer(t, map[string]func(http.ResponseWriter){
		"/api/embeddings": func(w http.ResponseWriter) {
			hits++
			json.NewEncoder(w).Encode(map[string]any{"embedding": []float32{4, 5}})
		},
	})
	o := newOllama(s.URL, "m")
	v, err := o.Embed(context.Background(), "hello")
	if err != nil || len(v) != 2 {
		t.Fatalf("legacy route not reached: %v, %v", v, err)
	}
	// The dialect is remembered, so the second call does not re-probe.
	if _, err := o.Embed(context.Background(), "again"); err != nil {
		t.Fatal(err)
	}
	if hits != 2 {
		t.Fatalf("want 2 legacy calls, got %d", hits)
	}
}

// A 200 carrying no vector is a server saying it cannot embed with this model.
// Returning a zero vector instead would make it a perfect neighbour of every
// other zero vector, which is a confident wrong answer.
func TestEmbed_EmptyVectorIsAnError(t *testing.T) {
	s := embedServer(t, map[string]func(http.ResponseWriter){
		"/api/embed": func(w http.ResponseWriter) {
			json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{}})
		},
		"/api/embeddings": func(w http.ResponseWriter) {
			json.NewEncoder(w).Encode(map[string]any{"embedding": []float32{}})
		},
	})
	if v, err := newOllama(s.URL, "m").Embed(context.Background(), "hello"); err == nil {
		t.Fatalf("want an error, got vector %v", v)
	}
}

func TestEmbed_ServerErrorIsReportedNotRetriedAsADialect(t *testing.T) {
	var calls int
	s := embedServer(t, map[string]func(http.ResponseWriter){
		"/api/embed": func(w http.ResponseWriter) {
			calls++
			http.Error(w, "model not found", http.StatusInternalServerError)
		},
		"/api/embeddings": func(w http.ResponseWriter) {
			calls++
			json.NewEncoder(w).Encode(map[string]any{"embedding": []float32{1}})
		},
	})
	if _, err := newOllama(s.URL, "m").Embed(context.Background(), "x"); err == nil {
		t.Fatal("a 500 must surface, not fall through to the other route")
	}
	if calls != 1 {
		t.Fatalf("want 1 call, got %d", calls)
	}
}

func TestTruncate_DoesNotSplitARune(t *testing.T) {
	s := strings.Repeat("é", 10) // 2 bytes each
	for n := range 21 {
		if got := truncate(s, n); !utf8Valid(got) {
			t.Fatalf("truncate(%d) split a rune: %q", n, got)
		}
	}
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

// --- Cosine ---

func TestCosine(t *testing.T) {
	cases := []struct {
		name string
		a, b []float32
		want float64
	}{
		{"identical", []float32{1, 2, 3}, []float32{1, 2, 3}, 1},
		{"orthogonal", []float32{1, 0}, []float32{0, 1}, 0},
		{"opposite", []float32{1, 0}, []float32{-1, 0}, -1},
		{"length mismatch", []float32{1, 0}, []float32{1, 0, 0}, 0},
		{"zero magnitude", []float32{0, 0}, []float32{1, 1}, 0},
		{"empty", nil, nil, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Cosine(c.a, c.b); got < c.want-1e-6 || got > c.want+1e-6 {
				t.Fatalf("want %v, got %v", c.want, got)
			}
		})
	}
}

// --- Recorder ---

// The guarantee the type exists for: a stalled embedder must not be felt by the
// dispatch that is being observed.
func TestRecorder_NeverBlocksOnAStalledEmbedder(t *testing.T) {
	gate := make(chan struct{})
	f := &fakeEmbedder{gate: gate}
	st := newTestStore(t, "fake")
	r := NewRecorder(f, st)

	start := time.Now()
	for i := range QueueDepth * 4 {
		r.Record(spanID(i), "some prompt")
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("Record blocked for %v; it must return at once", el)
	}
	_, dropped, _ := r.Counts()
	if dropped == 0 {
		t.Fatal("overflow must be counted as dropped, not silently absorbed")
	}
	close(gate)
	r.Close(5 * time.Second)
}

// Redaction is not advisory. A secret must not reach the model, and the test
// asserts the placeholder is present too, so deleting Redact fails here even if
// some other path happened to empty the text.
func TestRecorder_SecretNeverReachesTheEmbedder(t *testing.T) {
	const key = "AKIAIOSFODNN7EXAMPLE"
	f := &fakeEmbedder{}
	st := newTestStore(t, "fake")
	r := NewRecorder(f, st)
	r.Record(spanID(1), "deploy with "+key+" please")
	r.Close(5 * time.Second)

	seen := f.texts()
	if len(seen) != 1 {
		t.Fatalf("want 1 embed call, got %d", len(seen))
	}
	if strings.Contains(seen[0], key) {
		t.Fatalf("the secret reached the embedder: %q", seen[0])
	}
	if !strings.Contains(seen[0], "REDACTED") {
		t.Fatalf("want a redaction placeholder, got %q", seen[0])
	}
}

func TestRecorder_UnavailableEmbedderIsANoOp(t *testing.T) {
	st := newTestStore(t, "fake")
	r := NewRecorder(Unavailable{}, st)
	r.Record(spanID(1), "anything")
	r.Close(time.Second)
	if got := st.Stat().Count; got != 0 {
		t.Fatalf("want nothing stored, got %d", got)
	}
}

func TestRecorder_StoresWhatItEmbeds(t *testing.T) {
	f := &fakeEmbedder{vec: []float32{0.5, 0.5}}
	st := newTestStore(t, "fake")
	r := NewRecorder(f, st)
	r.Record(spanID(7), "a prompt")
	r.Close(5 * time.Second)

	stored, _, failed := r.Counts()
	if stored != 1 || failed != 0 {
		t.Fatalf("stored=%d failed=%d", stored, failed)
	}
	v, ok := st.Get(spanID(7))
	if !ok || len(v) != 2 {
		t.Fatalf("vector not retrievable: %v %v", v, ok)
	}
}

func TestRecorder_EmbedFailureIsCountedNotFatal(t *testing.T) {
	f := &fakeEmbedder{err: fmt.Errorf("model gone")}
	st := newTestStore(t, "fake")
	r := NewRecorder(f, st)
	r.Record(spanID(1), "x")
	r.Close(5 * time.Second)
	if _, _, failed := r.Counts(); failed != 1 {
		t.Fatalf("want 1 failure, got %d", failed)
	}
}

func TestRecorder_CloseIsIdempotent(t *testing.T) {
	r := NewRecorder(&fakeEmbedder{}, newTestStore(t, "fake"))
	r.Close(time.Second)
	r.Close(time.Second)
}
