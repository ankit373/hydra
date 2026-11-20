// SPDX-License-Identifier: MIT

package bench

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/executor"
)

// fakeOllama answers /v1/chat/completions as Ollama does and records what it was sent.
func fakeOllama(t *testing.T) (*httptest.Server, *[]*http.Request, *[]map[string]any) {
	t.Helper()
	var reqs []*http.Request
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		reqs, bodies = append(reqs, r), append(bodies, body)
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"qwen3:8b","choices":[{"message":{"role":"assistant","content":"yes"},
			"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OLLAMA_HOST", srv.URL)
	return srv, &reqs, &bodies
}

const localConfig = `
tier = "full"
track = "claims"
suites = ["fake"]
n_test = 5
seed = 1
max_usd = 0.01
arms = ["judge-local"]
[models]
judge-local = "ollama:qwen3:8b"
[cutoffs]
"qwen3:8b" = "unstated"
`

// A local arm reaches Ollama with no key, gets its answer and costs nothing, under a $0 cap.
func TestDirect_LocalOllamaArmAnswersWithNoKeyAndNoSpend(t *testing.T) {
	_, reqs, bodies := fakeOllama(t)
	c, err := ReadConfig(writeConfig(t, localConfig))
	if err != nil {
		t.Fatal(err)
	}
	o, err := c.Overrides()
	if err != nil {
		t.Fatal(err)
	}
	prices, err := Snapshot(nil, time.Time{}, c.ModelIDs(), c.PriceRefs, o)
	if err != nil {
		t.Fatal(err)
	}
	if p := prices["qwen3:8b"]; p.Ref != "local" || p.Source != "local" || p.InPerM != 0 || p.OutPerM != 0 {
		t.Fatalf("price row %+v, want a $0 row marked local", p)
	}
	arms, _ := c.ArmConfigs()
	d, err := NewDirect("judge-local", arms["judge-local"], prices, executor.For, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Preflight(context.Background()); err != nil {
		t.Fatalf("preflight: %v", err)
	}
	it := fakeItems(1)["test/000"]
	req, _ := fakeSuite{}.Prompt(it)
	att, err := d.Call(context.Background(), it, req, NewBudget(c.MaxUSD, 0), noSpend)
	if err != nil || att.Answer != "yes" || att.ErrKind != "" {
		t.Fatalf("attempt %+v, err %v", att, err)
	}
	if len(att.Calls) != 1 || att.Calls[0].CostUSD != 0 || att.Calls[0].PriceRef != "local" {
		t.Fatalf("calls %+v, want one $0 local call", att.Calls)
	}
	if len(*reqs) != 1 || (*reqs)[0].Header.Get("Authorization") != "" || (*bodies)[0]["model"] != "qwen3:8b" ||
		(*bodies)[0]["reasoning_effort"] != "none" {
		t.Fatalf("sent %d requests, auth %q, body %v", len(*reqs), (*reqs)[0].Header.Get("Authorization"), (*bodies)[0])
	}
}

// The snapshot is keyed by id, so one id on a local and a paid provider is refused, and
// a local model takes no list price.
func TestReadConfig_RefusesALocalIDThatCouldPriceAPaidOne(t *testing.T) {
	both := strings.Replace(localConfig, `arms = ["judge-local"]`, `arms = ["judge-local", "judge-paid"]`, 1)
	both = strings.Replace(both, `judge-local = "ollama:qwen3:8b"`,
		"judge-local = \"ollama:qwen3:8b\"\njudge-paid = \"openrouter:qwen3:8b\"", 1)
	if _, err := ReadConfig(writeConfig(t, both)); err == nil || !strings.Contains(err.Error(), "both") {
		t.Errorf("one id on ollama and openrouter: err %v", err)
	}
	priced, err := ReadConfig(writeConfig(t, localConfig+"[prices.\"qwen3:8b\"]\nin_per_mtok = 1\nout_per_mtok = 1\nsource = \"x\"\ndate = \"2026-11-16\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := priced.Overrides(); err == nil {
		t.Error("a list price on a local model accepted")
	}
}

func TestDetectionTable_LabelsALocalModel(t *testing.T) {
	s := Summary{Breadcrumb: Breadcrumb{Suite: "faithbench",
		Arms: map[string]ArmConfig{"judge-local": {Kind: KindDirect, Models: []string{"ollama:qwen3:8b"}}}},
		Arms: map[string]ArmSummary{"judge-local": {Detection: &DetectionStats{Positives: 1, Negatives: 1}}}}
	if got := DetectionTable([]Summary{s}); !strings.Contains(got, "| qwen3:8b (local) |") {
		t.Errorf("table does not say the model is local:\n%s", got)
	}
}
