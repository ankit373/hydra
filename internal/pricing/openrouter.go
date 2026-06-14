package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// openRouterModelsURL is a var so tests can override it with httptest.Server.
var openRouterModelsURL = "https://openrouter.ai/api/v1/models"

type orModel struct {
	ID      string    `json:"id"`
	Pricing orPricing `json:"pricing"`
}

type orPricing struct {
	// OpenRouter returns per-token cost as a decimal string, e.g. "0.000015"
	Prompt     string `json:"prompt"`
	Completion string `json:"completion"`
}

type orResponse struct {
	Data []orModel `json:"data"`
}

// fetchFromOpenRouter calls the OpenRouter models API and returns a map of
// model ID → ModelPrice (converted to per-million-token rates).
func fetchFromOpenRouter(ctx context.Context) (map[string]ModelPrice, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openRouterModelsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "hydra/1 (+https://github.com/ankit373/hydra)")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openrouter fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openrouter fetch: HTTP %d", resp.StatusCode)
	}

	var or_ orResponse
	if err := json.NewDecoder(resp.Body).Decode(&or_); err != nil {
		return nil, fmt.Errorf("openrouter parse: %w", err)
	}

	out := make(map[string]ModelPrice, len(or_.Data))
	for _, m := range or_.Data {
		id := strings.ToLower(m.ID)
		inp, errI := parsePerToken(m.Pricing.Prompt)
		out_, errO := parsePerToken(m.Pricing.Completion)
		if errI != nil || errO != nil {
			// Some free/experimental models have "0" or empty pricing — skip.
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
// Accepts "0.000015", "0", "", "-1" (free / not applicable → skip).
func parsePerToken(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty price")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if f < 0 {
		return 0, fmt.Errorf("negative price")
	}
	return f, nil
}
