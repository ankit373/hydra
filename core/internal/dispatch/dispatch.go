// Package dispatch routes a prompt through policy evaluation, head selection,
// and execution — with automatic fallback on failure.
package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/probe"
	"github.com/ankit373/hydra/internal/provider"
)

// Options controls dispatch behaviour.
type Options struct {
	TierHint  string // tier name from config, e.g. "standard"
	LocalOnly bool   // override: force local heads only
	MaxTokens int
	System    string
	DryRun    bool // print selected head without executing
}

// Result is the outcome of a successful dispatch.
type Result struct {
	Output    string
	Head      provider.Head
	Fallbacks []provider.Head // remaining candidates after the selected head
	Retries   int
	*executor.Response
}

// Dispatcher holds resolved config and the probed head list.
type Dispatcher struct {
	cfg    *config.Config
	heads  []provider.Head
	policy *policy.Engine
}

// New builds a Dispatcher from the saved config and a fresh machine probe.
func New(ctx context.Context) (*Dispatcher, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("no hydra config — run: hydra init")
	}

	result := probe.Run(ctx)

	localOnly := false
	if p, ok := cfg.Policies["pii"]; ok && p.Action == "local-only" {
		localOnly = true
	}

	return &Dispatcher{
		cfg:    cfg,
		heads:  result.Heads,
		policy: policy.New(policy.DefaultRules(localOnly)),
	}, nil
}

// Dispatch routes prompt through policy + tier selection + execution with fallback.
func (d *Dispatcher) Dispatch(ctx context.Context, prompt string, opts Options) (*Result, error) {
	req := policy.Request{Prompt: prompt, TierHint: opts.TierHint}
	action := d.policy.Evaluate(req)

	if action.Deny {
		return nil, fmt.Errorf("dispatch denied by policy: %s", action.Reason)
	}

	candidates := d.selectHeads(opts.TierHint, action.LocalOnly || opts.LocalOnly)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no available heads for tier %q (localOnly=%v)", opts.TierHint, opts.LocalOnly)
	}

	if opts.DryRun {
		return &Result{Head: candidates[0], Fallbacks: candidates[1:]}, nil
	}

	var lastErr error
	for i, h := range candidates {
		exec := executor.For(h)
		resp, err := exec.Execute(ctx, executor.Request{
			Prompt:    prompt,
			Head:      h,
			MaxTokens: opts.MaxTokens,
			System:    opts.System,
		})
		if err != nil {
			lastErr = err
			continue
		}
		r := &Result{Output: resp.Output, Head: h, Retries: i, Response: resp}
		_ = d.logDispatch(r, prompt)
		d.syncStateJSON(r)
		return r, nil
	}

	return nil, fmt.Errorf("all heads failed (tried %d): %w", len(candidates), lastErr)
}

// selectHeads returns heads to try, in order of preference.
//
// No tier hint → all probed heads ranked by score (config tiers ignored).
// Tier hint    → only heads assigned to that tier in config;
//
//	falls back to all probed heads if the tier has no live heads.
func (d *Dispatcher) selectHeads(tierHint string, localOnly bool) []provider.Head {
	filter := func(h provider.Head) bool {
		if !executor.Supports(h) {
			return false
		}
		if localOnly && !h.LocalOnly {
			return false
		}
		return true
	}

	// No tier specified — use every live head, best score first.
	if tierHint == "" {
		var all []provider.Head
		for _, h := range d.heads {
			if filter(h) {
				all = append(all, h)
			}
		}
		return all
	}

	// Collect head IDs assigned to the requested tier in config.
	tierIDs := map[string]bool{}
	for _, t := range d.cfg.Tiers {
		if t.Name == tierHint {
			for _, id := range t.Heads {
				tierIDs[id] = true
			}
		}
	}

	// Filter live probed heads to those in the tier.
	var candidates []provider.Head
	for _, h := range d.heads {
		if filter(h) && tierIDs[h.ID] {
			candidates = append(candidates, h)
		}
	}

	// Tier had no live heads — fall back to all available.
	if len(candidates) == 0 {
		return d.selectHeads("", localOnly)
	}
	return candidates
}

// syncStateJSON updates ~/.hydra/logs/state.json after a successful dispatch
// so the Ink UI (ui/) reflects Go dispatcher activity.
// It reads the existing file first to preserve claude_pct and exhausted_pools
// written by the shell router, then updates last_tier, last_model, last_status.
func (d *Dispatcher) syncStateJSON(r *Result) {
	stateDir := filepath.Join(config.Dir(), "logs")
	statePath := filepath.Join(stateDir, "state.json")

	// Read existing state to preserve shell-managed fields.
	existing := map[string]any{}
	if raw, err := os.ReadFile(statePath); err == nil {
		_ = json.Unmarshal(raw, &existing)
	}

	existing["last_model"] = r.Head.Name
	existing["last_status"] = "ok"
	if tier := r.Head.Meta["tier"]; tier != "" {
		existing["last_tier"] = tier
	}

	updated, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return
	}

	_ = os.MkdirAll(stateDir, 0o700)
	tmp := statePath + ".tmp"
	if err := os.WriteFile(tmp, updated, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, statePath)
}

// logDispatch appends a JSON record to ~/.hydra/dispatch.jsonl for analytics.
func (d *Dispatcher) logDispatch(r *Result, prompt string) error {
	entry := map[string]any{
		"ts":             time.Now().UTC().Format(time.RFC3339),
		"head":           r.Head.ID,
		"provider":       r.Head.Provider,
		"input_tokens":   r.Response.InputTokens,
		"output_tokens":  r.Response.OutputTokens,
		"duration_ms":    r.Response.Duration.Milliseconds(),
		"local":          r.Head.LocalOnly,
		"prompt_preview": truncate(prompt, 80),
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	path := filepath.Join(config.Dir(), "dispatch.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, string(raw))
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
