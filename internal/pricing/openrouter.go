package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// openRouterModelsURL is a var so tests can override it with httptest.Server.
var openRouterModelsURL = "https://openrouter.ai/api/v1/models"

// httpClient is package-level so connection pools are reused across calls.
var httpClient = &http.Client{Timeout: 10 * time.Second}

// maxResponseBytes caps the OpenRouter response body (5 MB should be ample
// for a models list; protects against runaway/adversarial responses).
const maxResponseBytes = 5 << 20

type orModel struct {
	ID      string    `json:"id"`
	Pricing orPricing `json:"pricing"`
}

type orPricing struct {
	// OpenRouter returns per-token cost as a decimal string, e.g. "0.000015".
	// Free models return "0". Models with unknown pricing return "".
	Prompt     string `json:"prompt"`
	Completion string `json:"completion"`
}

type orResponse struct {
	Data []orModel `json:"data"`
}

// fetchFromOpenRouter calls the OpenRouter models API and returns a map of
// model ID → ModelPrice (converted to per-million-token rates).
// Models with missing or negative pricing are skipped.
// Models with "0" pricing (free/local) are included with $0 rates.
func fetchFromOpenRouter(ctx context.Context) (map[string]ModelPrice, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openRouterModelsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "hydra/1 (+https://github.com/ankit373/hydra)")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openrouter fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openrouter fetch: HTTP %d", resp.StatusCode)
	}

	limited := io.LimitReader(resp.Body, maxResponseBytes+1)
	var or_ orResponse
	if err := json.NewDecoder(limited).Decode(&or_); err != nil {
		return nil, fmt.Errorf("openrouter parse: %w", err)
	}

	out := make(map[string]ModelPrice, len(or_.Data))
	for _, m := range or_.Data {
		id := strings.ToLower(m.ID)
		inp, errI := parsePerToken(m.Pricing.Prompt)
		out_, errO := parsePerToken(m.Pricing.Completion)
		if errI != nil || errO != nil {
			// Skip models with missing or negative pricing (empty string, "-1").
			// Models with explicit "0" pricing (free) are included.
			continue
		}
		out[id] = ModelPrice{
			InputPerMillion:  inp * 1_000_000,
			OutputPerMillion: out_ * 1_000_000,
		}
	}
	return out, nil
}

// parsePerToken converts an OpenRouter per-token price string to float64.
// Returns an error for empty strings and negative values (these models are
// skipped by fetchFromOpenRouter). Returns (0, nil) for "0" (free models).
func parsePerToken(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty price")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("non-finite price %q", s)
	}
	if f < 0 {
		return 0, fmt.Errorf("negative price %q", s)
	}
	return f, nil
}
