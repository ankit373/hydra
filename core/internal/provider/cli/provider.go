// Package cli discovers AI heads available as CLI tools on PATH.
// To add a new CLI tool: add one entry to the knownCLIs table — no other changes needed.
package cli

import (
	"context"
	"os/exec"

	"github.com/ankit373/hydra/internal/capabilities"
	"github.com/ankit373/hydra/internal/provider"
)

func init() { provider.Register(&Provider{}) }

type Provider struct{}

func (p *Provider) ID() string { return "cli" }

func (p *Provider) Discover(_ context.Context) ([]provider.Head, error) {
	caps, err := capabilities.Load("")
	if err != nil {
		return nil, err
	}

	var heads []provider.Head
	for _, c := range knownCLIs {
		path, err := exec.LookPath(c.binary)
		if err != nil {
			continue
		}
		heads = append(heads, provider.Head{
			ID:         c.binary,
			Name:       caps.Name(c.binary),
			Provider:   c.providerID,
			Source:     "cli",
			Executable: path,
			CapScore:   caps.Score(c.binary),
			LocalOnly:  c.local,
			AuthReady:  true,
		})
	}
	return heads, nil
}

// knownCLIs — add new CLI tools here. Name and score come from data.json.
var knownCLIs = []struct {
	binary     string
	providerID string
	local      bool
}{
	{"claude",     "anthropic",   false},
	{"codex",      "openai",      false},
	{"cursor",     "cursor",      false},
	{"kiro",       "amazon",      false}, // Amazon Kiro
	{"gemini",     "google",      false},
	{"windsurf",   "codeium",     false},
	{"amp",        "amp",         false},
	{"gh-copilot", "github",      false}, // gh extension install github/gh-copilot
	{"cody",       "sourcegraph", false},
	{"continue",   "continue",    false},
	{"agy",        "antigravity", false},
	{"ollama",     "local",       true},
	{"llamafile",  "local",       true},
}
