// SPDX-License-Identifier: MIT

// Package embed turns a prompt into a vector using an embedding model already
// running on the machine.
//
// It adds no dependency: Ollama is already discovered, already dialled and
// already serving embedding-only models that Hydra marks unroutable and then
// throws away (#532). This gives those heads their first job. With no such
// model the embedder is unavailable, which is a state callers must handle
// rather than an error, exactly as tier 10 is absent when Ollama is down (#248).
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ankit373/hydra/internal/provider"
)

// ErrUnavailable reports that no embedding model was resolved. Distinct from a
// failed call: unavailable is the normal state of a machine that has not pulled
// one, and every dependent feature is off rather than broken.
var ErrUnavailable = errors.New("embed: no embedding model available")

// Timeout bounds one embedding call. Generous because the first call to a cold
// model pays its load into memory, and because nothing waits on this: the
// recorder runs it off the dispatch path.
const Timeout = 30 * time.Second

// MaxInputBytes caps the text handed to a model. Past it the text is truncated
// on a rune boundary, since an embedding of the opening of a prompt is still a
// usable neighbour and a refused one is not.
const MaxInputBytes = 32 << 10

// Embedder produces a vector for text.
//
// Available reports whether a model was resolved at all, so a caller can skip
// the work rather than collect an error per dispatch.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	Available() bool
	Model() string
}

// Unavailable is the embedder a machine with no embedding model gets. Returning
// this rather than nil means callers never nil-check before asking.
type Unavailable struct{}

func (Unavailable) Embed(context.Context, string) ([]float32, error) {
	return nil, ErrUnavailable
}
func (Unavailable) Available() bool { return false }
func (Unavailable) Model() string   { return "" }

var client = &http.Client{Timeout: Timeout}

// Resolve picks an embedding model from already-discovered heads.
//
// Only a head the server itself reported as embedding-only is taken. An older
// Ollama reports no capabilities at all, so its embedding models look routable
// and are not found here; naming one in config is the way through, because
// guessing from a model name would fabricate a capability the server never
// claimed.
func Resolve(heads []provider.Head, configured string) Embedder {
	if configured != "" {
		for _, h := range heads {
			if modelOf(h) == configured && h.Endpoint != "" {
				return newOllama(h.Endpoint, configured)
			}
		}
		// Named but not discovered. Honour the name against the default host:
		// the operator knows something discovery does not, which is the only
		// reason to write it down.
		return newOllama(provider.OllamaHost(), configured)
	}
	for _, h := range heads {
		if h.Meta["embedding_only"] == "true" && h.Endpoint != "" {
			return newOllama(h.Endpoint, modelOf(h))
		}
	}
	return Unavailable{}
}

// modelOf recovers the server-side model name from a head id like
// "ollama/nomic-embed-text".
func modelOf(h provider.Head) string {
	if m := h.Meta["model"]; m != "" {
		return m
	}
	if _, name, ok := strings.Cut(h.ID, "/"); ok {
		return name
	}
	return h.ID
}

// ollama speaks whichever of Ollama's two embedding routes the server answers.
type ollama struct {
	base  string
	model string

	mu      sync.Mutex
	dialect string // "" until the first successful call decides
}

func newOllama(base, model string) *ollama {
	return &ollama{base: strings.TrimRight(base, "/"), model: model}
}

func (o *ollama) Available() bool { return o.model != "" }
func (o *ollama) Model() string   { return o.model }

// Embed vectorises text.
//
// Ollama moved embeddings from /api/embeddings to /api/embed and kept both for
// a while. Which one answers is a property of the installed server, so it is
// discovered once and remembered, not assumed from a version we never read.
func (o *ollama) Embed(ctx context.Context, text string) ([]float32, error) {
	if !o.Available() {
		return nil, ErrUnavailable
	}
	text = truncate(text, MaxInputBytes)
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("embed: empty input")
	}

	o.mu.Lock()
	dialect := o.dialect
	o.mu.Unlock()

	order := []string{"embed", "embeddings"}
	if dialect != "" {
		order = []string{dialect}
	}

	var lastErr error
	for _, d := range order {
		v, err := o.call(ctx, d, text)
		if err == nil {
			o.mu.Lock()
			o.dialect = d
			o.mu.Unlock()
			return v, nil
		}
		lastErr = err
		// A transport or context failure says nothing about which route exists,
		// so trying the other one would only mistake an outage for a dialect.
		if ctx.Err() != nil || !errors.Is(err, errWrongRoute) {
			return nil, err
		}
	}
	return nil, lastErr
}

// errWrongRoute marks the one failure that justifies trying the other route.
var errWrongRoute = errors.New("embed: route not served")

func (o *ollama) call(ctx context.Context, dialect, text string) ([]float32, error) {
	var path string
	var body any
	switch dialect {
	case "embed":
		path, body = "/api/embed", map[string]any{"model": o.model, "input": text}
	default:
		path, body = "/api/embeddings", map[string]any{"model": o.model, "prompt": text}
	}

	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.base+path, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return nil, fmt.Errorf("%w: %s returned %d", errWrongRoute, path, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("embed: %s returned %d: %s", path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var out struct {
		Embedding  []float32   `json:"embedding"`
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("embed: decoding %s: %w", path, err)
	}

	v := out.Embedding
	if len(out.Embeddings) > 0 {
		v = out.Embeddings[0]
	}
	if len(v) == 0 {
		// A 200 with no vector is the shape a server uses to report a model it
		// cannot embed with. Reported as an error, never as a zero vector,
		// which would be a perfect neighbour of every other zero vector.
		return nil, fmt.Errorf("embed: %s returned no vector for %q", path, o.model)
	}
	return v, nil
}

// truncate cuts text to at most n bytes without splitting a rune.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !isRuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
