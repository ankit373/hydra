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
	"time"

	"github.com/ankit373/hydra/internal/capabilities"
	"github.com/ankit373/hydra/internal/provider"
)

func init() { provider.Register(&Provider{}) }

var httpClient = &http.Client{Timeout: 2 * time.Second}

// portService probes a specific port and returns discovered Heads.
type portService interface {
	port() int
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

	services := []portService{
		&ollamaService{},
		&lmStudioService{},
	}

	var heads []provider.Head
	for _, svc := range services {
		if !isOpen(svc.port()) {
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

func isOpen(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 400*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// ── Ollama ────────────────────────────────────────────────────────────────────

type ollamaService struct{}

func (s *ollamaService) port() int { return 11434 }

func (s *ollamaService) probe(ctx context.Context, caps *capabilities.DB) ([]provider.Head, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost:11434/api/tags", nil)
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
			ID:        "ollama/" + m.Name,
			Name:      m.Name + " (Ollama)",
			Provider:  "local",
			Source:    "port",
			Endpoint:  "http://localhost:11434",
			CapScore:  caps.ScoreOllama(m.Name),
			LocalOnly: true,
			AuthReady: true,
		})
	}
	return heads, nil
}

// ── LM Studio ─────────────────────────────────────────────────────────────────

type lmStudioService struct{}

func (s *lmStudioService) port() int { return 1234 }

func (s *lmStudioService) probe(ctx context.Context, caps *capabilities.DB) ([]provider.Head, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost:1234/v1/models", nil)
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
			Endpoint:  "http://localhost:1234",
			CapScore:  caps.ScoreOllama(m.ID),
			LocalOnly: true,
			AuthReady: true,
		})
	}
	return heads, nil
}
