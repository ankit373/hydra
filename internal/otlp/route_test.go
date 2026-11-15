// SPDX-License-Identifier: MIT

package otlp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/waterfall"
)

func TestExport_RouteAttributes(t *testing.T) {
	task := runlog.SpanIDFor("task-abc")
	events := append(fallbackRun(), runlog.Event{
		V: 2, Seq: 7, TS: "2026-09-01T10:00:00Z", RunID: "run-xyz", TaskID: "task-abc",
		Kind: runlog.KindRoute, SpanID: task, Route: &runlog.Route{
			Objective: "cost", Requirement: 0.9, FirstHead: "agy/opus", FirstProb: 0.95,
			RunnerUp: "ollama/qwen3", Omitted: 3,
			Candidates: []runlog.RouteCandidate{
				{Head: "agy/opus", Tier: 2, Effective: 93, N: 40, InDomain: 25, Clears: true, CostUSD: 0.02, Gate: "passed"},
				{Head: "ollama/qwen3", Tier: 10, Effective: 61, Gate: "passed"},
				{Head: "env/groq", Gate: "not_local"},
			},
		},
	})
	p, err := Build([]*waterfall.Trace{waterfall.Build(events)}, nil, "hydra", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, a := range spansByID(p)[task].Attributes {
		if strings.HasPrefix(a.Key, "hydra.route.") {
			raw, _ := json.Marshal(a.Value)
			got[a.Key] = string(raw)
		}
	}
	want := map[string]string{
		"hydra.route.objective":           `{"stringValue":"cost"}`,
		"hydra.route.requirement":         `{"doubleValue":0.9}`,
		"hydra.route.first_head":          `{"stringValue":"agy/opus"}`,
		"hydra.route.first_prob":          `{"doubleValue":0.95}`,
		"hydra.route.runner_up":           `{"stringValue":"ollama/qwen3"}`,
		"hydra.route.omitted":             `{"intValue":"3"}`,
		"hydra.route.candidate.head":      `{"arrayValue":{"values":[{"stringValue":"agy/opus"},{"stringValue":"ollama/qwen3"},{"stringValue":"env/groq"}]}}`,
		"hydra.route.candidate.effective": `{"arrayValue":{"values":[{"intValue":"93"},{"intValue":"61"},{"intValue":"0"}]}}`,
		"hydra.route.candidate.n":         `{"arrayValue":{"values":[{"intValue":"40"},{"intValue":"0"},{"intValue":"0"}]}}`,
		"hydra.route.candidate.clears":    `{"arrayValue":{"values":[{"boolValue":true},{"boolValue":false},{"boolValue":false}]}}`,
		"hydra.route.candidate.cost_usd":  `{"arrayValue":{"values":[{"doubleValue":0.02},{"doubleValue":0},{"doubleValue":0}]}}`,
		"hydra.route.candidate.gate":      `{"arrayValue":{"values":[{"stringValue":"passed"},{"stringValue":"passed"},{"stringValue":"not_local"}]}}`,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %s, want %s", k, got[k], v)
		}
	}
	for _, k := range []string{"model", "tier", "in_domain"} {
		if _, ok := got["hydra.route.candidate."+k]; !ok {
			t.Errorf("hydra.route.candidate.%s is missing", k)
		}
	}

	// A span with no route carries none of it.
	for _, a := range spansByID(p)["2222222222222222"].Attributes {
		if strings.HasPrefix(a.Key, "hydra.route.") {
			t.Errorf("attempt span carries %s", a.Key)
		}
	}
}
