// SPDX-License-Identifier: MIT

package swarm

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/cost"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/runid"
)

// PricingReader abstracts the per-tier cost lookup so swarm doesn't need to
// re-parse pricing.yaml itself.
type PricingReader interface {
	// EstimateCost returns the estimated USD cost for the given tier + token counts.
	EstimateCost(tier int, inputTokens, outputTokens int) float64
}

// estimateFanoutCost estimates the total USD cost of firing every selected
// head once. Uses prompt character count / 4 as a token estimate (same as the
// agy executor).
//
// Separate from preflightCost because a *guard* can skip the arithmetic when no
// limit is set, but a *plan* cannot: --dry-run must report the cost even when
// nothing caps it (#167).
func estimateFanoutCost(heads []provider.Head, prompt string, pr PricingReader) float64 {
	if pr == nil {
		return 0
	}
	estInputTokens := len(prompt) / 4
	var total float64
	for _, h := range heads {
		total += pr.EstimateCost(rank.UITier(h), estInputTokens, estInputTokens/2)
	}
	return round6(total)
}

// preflightCost estimates the total cost of firing all selected heads before
// any execution begins. Returns an error if the estimate exceeds maxUSD.
func preflightCost(heads []provider.Head, prompt string, pr PricingReader, maxUSD float64) (float64, error) {
	if pr == nil || maxUSD <= 0 {
		return 0, nil
	}
	total := estimateFanoutCost(heads, prompt, pr)
	if total > maxUSD {
		return total, fmt.Errorf("swarm: estimated cost $%.4f exceeds limit $%.4f (%d heads)", total, maxUSD, len(heads))
	}
	return total, nil
}

// enrichCosts fills EstCostUSD on each attempt using real token counts.
func enrichCosts(attempts []Attempt, pr PricingReader) {
	if pr == nil {
		return
	}
	for i := range attempts {
		if attempts[i].Status == StatusOK {
			attempts[i].EstCostUSD = round6(pr.EstimateCost(rank.UITier(attempts[i].Head), attempts[i].InputTokens, attempts[i].OutputTokens))
		}
	}
}

// logAttempts writes one cost.jsonl entry per attempt that actually executed
// (StatusOK or StatusFailed — not Pending/Canceled).
//
// It takes the attempts and mode directly rather than a *SwarmResult so the SPRT
// path can share it: RunSPRT produces attempts without ever building a
// SwarmResult, and without this its ensemble spend never reached cost.jsonl at
// all — only the aggregate trust.jsonl row (#175).
//
// Every attempt shares the run's identity: heads racing or voting on one prompt
// are all working the same logical task, so they carry the same TaskID. That is
// what lets a reader group "5 heads on one task" rather than seeing 5 unrelated
// rows (#181).
func logAttempts(attempts []Attempt, mode SwarmMode, opts Options, promptPreview string) {
	logDir := filepath.Join(config.Dir(), "logs")
	_ = os.MkdirAll(logDir, 0o700)
	path := filepath.Join(logDir, "cost.jsonl")

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()

	runID := runid.ResolveRun(opts.RunID)
	taskID := runid.ResolveTask(opts.TaskID)
	breadcrumb, _ := config.Breadcrumb()

	for _, a := range attempts {
		if a.Status == StatusPending || a.Status == StatusCanceled {
			continue
		}
		// Shared with the dispatch log path — see cost.SourceLabels.
		tokensSource, costSrc, legacySource := cost.SourceLabels(a.TokensEstimated)
		entry := map[string]any{
			"ts":              a.FinishedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
			"tier":            rank.UITier(a.Head),
			"model":           a.Head.Name,
			"executor":        a.Head.Provider,
			"pool":            a.Head.Meta["token_pool"],
			"prompt_tokens":   a.InputTokens,
			"response_tokens": a.OutputTokens,
			"est_cost_usd":    a.EstCostUSD,
			"wall_ms":         a.Duration.Milliseconds(),
			"tokens_source":   tokensSource,
			"cost_source":     costSrc,
			"source":          legacySource,
			"swarm_mode":      string(mode),
			"swarm_winner":    a.Status == StatusOK && a.Rank == 1,
			"task_id":         taskID,
			"run_id":          runID,
			"prompt_preview":  promptPreview,
		}
		if breadcrumb != "" { // match the omitempty on cost.Row.Config
			entry["config"] = breadcrumb
		}
		raw, _ := json.Marshal(entry)
		_, _ = fmt.Fprintln(f, string(raw))
	}
}

func round6(f float64) float64 {
	return math.Round(f*1_000_000) / 1_000_000
}
