// SPDX-License-Identifier: MIT

package port

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ankit373/hydra/internal/capabilities"
	"github.com/ankit373/hydra/internal/provider"
)

// DefaultLlamaCppHost is where llama-server listens unless told otherwise.
const DefaultLlamaCppHost = "http://127.0.0.1:8080"

const llamaCppPort = 8080

// llamaCppService discovers a running llama.cpp server.
//
// 8080 is the most common port on a developer's machine, so this identifies the
// server rather than inferring it from the address: /props carries build_info,
// which nothing else serves. Copying LM Studio's "something answered
// /v1/models" would offer a stranger's service to the router as a local head.
type llamaCppService struct {
	base string
	key  string
}

func newLlamaCppService() *llamaCppService {
	return &llamaCppService{base: llamaCppHost(), key: strings.TrimSpace(os.Getenv("LLAMA_API_KEY"))}
}

// llamaCppHost resolves where llama-server is listening from llama.cpp's own
// variables, so a machine already configured for it needs nothing new.
//
// These are *bind* settings, not a client address: a server told to listen on
// 0.0.0.0 is reachable at localhost, and dialling 0.0.0.0 is a different
// question than the one the probe is asking.
func llamaCppHost() string {
	host, port := strings.TrimSpace(os.Getenv("LLAMA_ARG_HOST")), strings.TrimSpace(os.Getenv("LLAMA_ARG_PORT"))
	if host == "" && port == "" {
		return DefaultLlamaCppHost
	}
	if host == "" || isUnspecified(host) {
		host = "127.0.0.1"
	}
	if port == "" {
		port = strconv.Itoa(llamaCppPort)
	}
	if _, err := strconv.Atoi(port); err != nil {
		return DefaultLlamaCppHost
	}
	return "http://" + net.JoinHostPort(host, port)
}

// isUnspecified reports a bind-any address, which names every interface rather
// than one to connect to.
func isUnspecified(host string) bool {
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsUnspecified()
}

func (s *llamaCppService) addr() string { return hostPort(s.base, llamaCppPort) }

func (s *llamaCppService) probe(ctx context.Context, caps *capabilities.DB) ([]provider.Head, error) {
	props, err := s.props(ctx)
	if err != nil {
		return nil, err
	}

	models, err := s.models(ctx)
	if err != nil {
		return nil, err
	}

	heads := make([]provider.Head, 0, len(models))
	for _, id := range models {
		heads = append(heads, s.head(id, props, caps))
	}
	return heads, nil
}

// llamaCppProps is the part of /props this reads. BuildInfo is the identity:
// measured as "b11042-ec9281505" on a real server, and absent from anything
// else that might be listening on 8080.
type llamaCppProps struct {
	BuildInfo  string `json:"build_info"`
	Generation struct {
		// The window the server actually allocated, which is the -c it was
		// started with. Reported rather than declared, so it replaces a guess
		// instead of capping one.
		NCtx int `json:"n_ctx"`
	} `json:"default_generation_settings"`
}

func (s *llamaCppService) props(ctx context.Context) (llamaCppProps, error) {
	var p llamaCppProps
	body, err := s.get(ctx, "/props")
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return p, fmt.Errorf("llamacpp probe: /props is not llama.cpp's: %w", err)
	}
	if p.BuildInfo == "" {
		return p, fmt.Errorf("llamacpp probe: /props carries no build_info, so this is not llama.cpp")
	}
	return p, nil
}

func (s *llamaCppService) models(ctx context.Context) ([]string, error) {
	body, err := s.get(ctx, "/v1/models")
	if err != nil {
		return nil, err
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(payload.Data))
	for _, m := range payload.Data {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	return out, nil
}

func (s *llamaCppService) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+path, nil)
	if err != nil {
		return nil, err
	}
	// llama-server's own --api-key, read from the variable it documents.
	if s.key != "" {
		req.Header.Set("Authorization", "Bearer "+s.key)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("llamacpp probe: %s returned %d", path, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

func (s *llamaCppService) head(id string, props llamaCppProps, caps *capabilities.DB) provider.Head {
	name := llamaCppModelName(id)
	meta := map[string]string{
		// The id the server answers to, which is the file path it loaded
		// unless -a named an alias. Not derivable from the head id, since that
		// carries the readable name.
		"model":        id,
		"model_source": caps.SourceOllama(name),
	}
	if props.Generation.NCtx > 0 {
		meta["model_ctx"] = strconv.Itoa(props.Generation.NCtx)
	}
	return provider.Head{
		ID:        "llamacpp/" + name,
		Name:      name + " (llama.cpp)",
		Provider:  "llamacpp",
		Source:    "port",
		Endpoint:  s.base,
		CapScore:  caps.ScoreOllama(name),
		LocalOnly: true,
		AuthReady: true,
		Meta:      meta,
	}
}

// llamaCppModelName turns the served id into something readable. llama-server
// reports the file it loaded, so the id is a path unless -a named an alias:
// the base name without .gguf is what a person calls that model.
func llamaCppModelName(id string) string {
	name := strings.TrimSuffix(filepath.Base(id), ".gguf")
	if name == "" || name == "." || name == string(filepath.Separator) {
		return id
	}
	return name
}
