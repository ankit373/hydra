package swarm

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/provider"
)

// PricingReader abstracts the per-tier cost lookup so swarm doesn't need to
// re-parse pricing.yaml itself.
type PricingReader interface {
	// EstimateCost returns the estimated USD cost for the given tier + token counts.
	EstimateCost(tier int, inputTokens, outputTokens int) float64
}

// preflightCost estimates the total cost of firing all selected heads before
// any execution begins. Returns an error if the estimate exceeds maxUSD.
// Uses prompt character count / 4 as a token estimate (same as agy executor).
func preflightCost(heads []provider.Head, prompt string, pr PricingReader, maxUSD float64) (float64, error) {
	if pr == nil || maxUSD <= 0 {
		return 0, nil
	}

	estInputTokens := len(prompt) / 4
	var total float64
	for _, h := range heads {
		tier := uiTier(h)
		// Estimate: same tokens in, half tokens out (conservative).
		total += pr.EstimateCost(tier, estInputTokens, estInputTokens/2)
	}

	total = round6(total)
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
			tier := uiTier(attempts[i].Head)
			attempts[i].EstCostUSD = round6(pr.EstimateCost(tier, attempts[i].InputTokens, attempts[i].OutputTokens))
		}
	}
}

// logAttempts writes one cost.jsonl entry per attempt that actually executed
// (StatusOK or StatusFailed — not Pending/Canceled).
func logAttempts(result *SwarmResult, promptPreview string) {
	logDir := filepath.Join(config.Dir(), "logs")
	_ = os.MkdirAll(logDir, 0o700)
	path := filepath.Join(logDir, "cost.jsonl")

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()

	runID := os.Getenv("HYDRA_RUN_ID")
	taskID := os.Getenv("HYDRA_TASK_ID")

	for _, a := range result.Attempts {
		if a.Status == StatusPending || a.Status == StatusCanceled {
			continue
		}
		entry := map[string]any{
			"ts":              time.Now().UTC().Format(time.RFC3339),
			"tier":            uiTier(a.Head),
			"model":           a.Head.Name,
			"executor":        a.Head.Provider,
			"pool":            a.Head.Meta["token_pool"],
			"prompt_tokens":   a.InputTokens,
			"response_tokens": a.OutputTokens,
			"est_cost_usd":    a.EstCostUSD,
			"wall_ms":         a.Duration.Milliseconds(),
			"source":          costSource(a),
			"swarm_mode":      string(result.Mode),
			"swarm_winner":    a.Status == StatusOK && a.Rank == 1,
			"task_id":         taskID,
			"run_id":          runID,
			"prompt_preview":  promptPreview,
		}
		raw, _ := json.Marshal(entry)
		_, _ = fmt.Fprintln(f, string(raw))
	}
}

func costSource(a Attempt) string {
	if a.InputTokens > 0 {
		return "real"
	}
	return "estimate"
}

// uiTier converts a Head's CapScore to a 1-10 tier integer.
// Mirrors the same function in dispatch.go — kept local to avoid a circular import.
func uiTier(h provider.Head) int {
	if h.Source == "registry" {
		if t := h.Meta["tier"]; t != "" {
			var n int
			if _, err := fmt.Sscanf(t, "%d", &n); err == nil {
				return n
			}
		}
	}
	switch {
	case h.CapScore >= 95:
		return 1
	case h.CapScore >= 90:
		return 2
	case h.CapScore >= 85:
		return 3
	case h.CapScore >= 80:
		return 4
	case h.CapScore >= 78:
		return 5
	case h.CapScore >= 72:
		return 6
	case h.CapScore >= 70:
		return 7
	case h.CapScore >= 65:
		return 8
	case h.CapScore >= 60:
		return 9
	default:
		return 10
	}
}

func round6(f float64) float64 {
	return math.Round(f*1_000_000) / 1_000_000
}
