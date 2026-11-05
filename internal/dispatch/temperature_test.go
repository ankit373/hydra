// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/health"
	"github.com/ankit373/hydra/internal/provider"
)

// G7. A served temperature has no echo header, so the provider body is the
// only place it can be proven to have arrived (E7 runs every arm at 0).
func TestDispatch_TemperatureReachesTheWire(t *testing.T) {
	zero, warm := 0.0, 0.7
	for _, sent := range []*float64{nil, &zero, &warm} {
		var got map[string]json.RawMessage
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&got)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
		}))
		d := streamDispatcher(t, provider.Head{
			ID: "wire", Name: "wire", Provider: "openai", Source: "env",
			Endpoint: srv.URL, AuthReady: true, CapScore: 90,
		})
		_, err := d.Dispatch(context.Background(), "hi", Options{Temperature: sent, RunID: "run-temp", TaskID: "task-temp"})
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}

		raw, present := got["temperature"]
		var received *float64
		if present {
			var v float64
			if err := json.Unmarshal(raw, &v); err != nil {
				t.Fatal(err)
			}
			received = &v
		}
		if (received == nil) != (sent == nil) || (sent != nil && *received != *sent) {
			t.Errorf("head received temperature %v, client sent %v", showTemp(received), showTemp(sent))
		}
	}
}

// G7c. Anthropic caps temperature at 1. Sending it 1.5 earns a 400 that the
// breaker would read as the head failing, so it is skipped, not tried.
func TestDispatch_TemperatureAboveAHeadsRangeSkipsThatHead(t *testing.T) {
	var got map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer srv.Close()
	capped := cappedHead(t)
	wide := provider.Head{ID: "wire", Name: "wire", Provider: "openai", Source: "env", Endpoint: srv.URL, AuthReady: true, CapScore: 90}

	healthFile := filepath.Join(t.TempDir(), "health.json")
	d := streamDispatcher(t, capped, wide)
	d.health = health.Open(healthFile)
	warm := 1.5
	res, err := d.Dispatch(context.Background(), "hi", Options{Temperature: &warm, RunID: "run-warm", TaskID: "task-warm"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Head.ID != "wire" || string(got["temperature"]) != "1.5" {
		t.Errorf("answered by %s with temperature %s, want wire at 1.5", res.Head.ID, got["temperature"])
	}
	if len(res.Attempts) != 1 || res.Attempts[0].Head != capped.ID || !strings.Contains(res.Attempts[0].Reason, "temperature") {
		t.Errorf("attempts %+v, want %s skipped for its temperature range", res.Attempts, capped.ID)
	}
	_ = d.health.Flush()
	if raw, _ := os.ReadFile(healthFile); strings.Contains(string(raw), capped.ID) {
		t.Errorf("the breaker recorded a failure for a head the request was never sent to: %s", raw)
	}
}

// When no candidate can take the temperature it is the request that cannot be
// asked, which serve answers 400 rather than 502.
func TestDispatch_TemperatureNoHeadAcceptsIsUnaskable(t *testing.T) {
	capped := cappedHead(t)
	d := streamDispatcher(t, capped)
	warm := 1.5
	_, err := d.Dispatch(context.Background(), "hi", Options{Temperature: &warm, RunID: "run-warm2", TaskID: "task-warm2"})
	if !errors.Is(err, executor.ErrUnaskable) || !strings.Contains(fmt.Sprint(err), "1.5") {
		t.Errorf("got %v, want ErrUnaskable naming temperature 1.5", err)
	}
}

// Some heads cannot take the temperature and the rest exceed the ceiling: all
// the request's doing, so still unaskable rather than a head failure.
func TestDispatch_TemperatureAndCeilingTogetherAreUnaskable(t *testing.T) {
	wide := provider.Head{ID: "wire", Name: "wire", Provider: "openai", Source: "env", Endpoint: "http://127.0.0.1:1", AuthReady: true, CapScore: 90}
	d := streamDispatcher(t, cappedHead(t), wide)
	warm := 1.5
	_, err := d.Dispatch(context.Background(), strings.Repeat("x", 400), Options{
		Temperature: &warm, MaxCostUSD: 1e-12, RunID: "run-mix", TaskID: "task-mix",
	})
	if !errors.Is(err, executor.ErrUnaskable) || !strings.Contains(fmt.Sprint(err), "cost ceiling") {
		t.Errorf("got %v, want ErrUnaskable naming both the temperature and the ceiling", err)
	}
}

func TestMaxTemperature_ByDialect(t *testing.T) {
	for _, tc := range []struct {
		head provider.Head
		want float64
	}{
		{provider.Head{Provider: "anthropic", Source: "env"}, 1},
		{provider.Head{Provider: "cohere", Source: "env"}, 1},
		{provider.Head{Provider: "bedrock", Source: "env"}, 1},
		{provider.Head{Provider: "openai", Source: "env"}, 2},
		{provider.Head{Provider: "google", Source: "env"}, 2},
		{provider.Head{Provider: "anthropic", Source: "cli"}, 2},
	} {
		if got := executor.MaxTemperature(tc.head); got != tc.want {
			t.Errorf("MaxTemperature(%s via %s) = %v, want %v", tc.head.Provider, tc.head.Source, got, tc.want)
		}
	}
}

// Every head over the ceiling is graded, so serve can tell a client's own
// limit from a failed head, while the CLI's message stays exactly as it was.
func TestDispatch_EveryHeadOverTheCeilingIsGraded(t *testing.T) {
	d := streamDispatcher(t, provider.Head{ID: "wire", Name: "wire", Provider: "openai", Source: "env", Endpoint: "http://127.0.0.1:1", AuthReady: true, CapScore: 90})
	_, err := d.Dispatch(context.Background(), strings.Repeat("x", 400), Options{MaxCostUSD: 1e-12, MaxCostSource: "X", RunID: "run-c", TaskID: "task-c"})
	if !errors.Is(err, ErrOverCostCeiling) {
		t.Fatalf("got %v, want ErrOverCostCeiling", err)
	}
	if !strings.HasPrefix(err.Error(), "all heads failed (tried 1): estimated cost $") {
		t.Errorf("the message changed: %v", err)
	}
}

func showTemp(f *float64) string {
	if f == nil {
		return "none"
	}
	return fmt.Sprint(*f)
}

// cappedHead is a routable Anthropic head whose endpoint refuses connections,
// so a request that reached it would fail rather than leave the machine.
func cappedHead(t *testing.T) provider.Head {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "test-only")
	t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:1")
	return provider.Head{ID: "env/anthropic", Name: "claude", Provider: "anthropic", Source: "env", AuthReady: true, CapScore: 99}
}
