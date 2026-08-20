// SPDX-License-Identifier: MIT

// Package port discovers AI heads running as local HTTP services.
// To add a new local service: implement a portService and add it to services.
package port

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/ankit373/hydra/internal/capabilities"
	"github.com/ankit373/hydra/internal/provider"
)

func init() { provider.Register(&Provider{}) }

var httpClient = &http.Client{Timeout: 2 * time.Second}

// portService probes a specific address and returns discovered Heads.
//
// addr is a host:port, not a bare port: $OLLAMA_HOST can move Ollama to another
// host as well as another port, and a liveness check aimed at the wrong address
// answers a different question than the probe that follows it (#282).
type portService interface {
	addr() string
	probe(ctx context.Context, caps *capabilities.DB) ([]provider.Head, error)
}

// Provider scans well-known local ports for running model servers.
type Provider struct{}

func (p *Provider) ID() string { return "port" }

func (p *Provider) Discover(ctx context.Context) ([]provider.Head, error) {
	caps, err := capabilities.Load(capabilities.DefaultOverlayPath())
	if err != nil {
		return nil, err
	}

	// Base URLs are resolved here, once, and handed to the services — so a
	// service can be constructed against a test server, and so the address the
	// liveness dial uses is provably the same one the probe and the head's
	// Endpoint use.
	services := []portService{
		&ollamaService{base: provider.OllamaHost()},
		&lmStudioService{base: defaultLMStudioHost},
	}

	var heads []provider.Head
	for _, svc := range services {
		if !isOpen(svc.addr()) {
			continue
		}
		found, err := svc.probe(ctx, caps)
		if err != nil {
			continue // service is up but probe failed; skip gracefully
		}
		heads = append(heads, found...)
	}
	return heads, nil
}

// isOpen is a cheap liveness check before the real probe, so a machine with
// nothing listening does not pay the HTTP client's full timeout per service.
func isOpen(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 400*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// hostPort extracts "host:port" from a base URL, supplying defaultPort when the
// URL carries none. The dial and the HTTP probe must target the same place;
// deriving one from the other is what keeps them in step.
func hostPort(base string, defaultPort int) string {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return fmt.Sprintf("localhost:%d", defaultPort)
	}
	if u.Port() != "" {
		return u.Host
	}
	return net.JoinHostPort(u.Hostname(), fmt.Sprintf("%d", defaultPort))
}

// ── Ollama ────────────────────────────────────────────────────────────────────

// ollamaService is constructed with the base URL $OLLAMA_HOST resolves to.
// This used to hardcode localhost:11434 while the executor honoured the
// variable, so a user running Ollama on any other address had a working server
// discovery could not see: no tier-10 head, and a silent degrade to a paid
// one (#282).
type ollamaService struct{ base string }

func (s *ollamaService) addr() string { return hostPort(s.base, 11434) }

func (s *ollamaService) probe(ctx context.Context, caps *capabilities.DB) ([]provider.Head, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama probe: unexpected status %d", resp.StatusCode)
	}

	var payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return nil, err
	}

	heads := make([]provider.Head, 0, len(payload.Models))
	for _, m := range payload.Models {
		heads = append(heads, provider.Head{
			ID:       "ollama/" + m.Name,
			Name:     m.Name + " (Ollama)",
			Provider: "local",
			Source:   "port",
			// The address it was found at, not a constant. The executor dials
			// this later, so a head discovered at one address and stamped with
			// another fails at the point of use.
			Endpoint:  s.base,
			CapScore:  caps.ScoreOllama(m.Name),
			LocalOnly: true,
			AuthReady: true,
			Meta:      map[string]string{"model_source": caps.SourceOllama(m.Name)},
		})
	}
	return heads, nil
}

// ── LM Studio ─────────────────────────────────────────────────────────────────

// defaultLMStudioHost is LM Studio's default server address. Unlike Ollama it
// publishes no environment variable for relocating it, so there is nothing to
// honour — but the base is still a field so the two services behave the same
// way and both are testable against a stub server.
const defaultLMStudioHost = "http://localhost:1234"

type lmStudioService struct{ base string }

func (s *lmStudioService) addr() string { return hostPort(s.base, 1234) }

func (s *lmStudioService) probe(ctx context.Context, caps *capabilities.DB) ([]provider.Head, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lmstudio probe: unexpected status %d", resp.StatusCode)
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return nil, err
	}

	heads := make([]provider.Head, 0, len(payload.Data))
	for _, m := range payload.Data {
		heads = append(heads, provider.Head{
			ID:        "lmstudio/" + m.ID,
			Name:      m.ID + " (LM Studio)",
			Provider:  "local",
			Source:    "port",
			Endpoint:  s.base,
			CapScore:  caps.ScoreOllama(m.ID),
			LocalOnly: true,
			AuthReady: true,
			Meta:      map[string]string{"model_source": caps.SourceOllama(m.ID)},
		})
	}
	return heads, nil
}
