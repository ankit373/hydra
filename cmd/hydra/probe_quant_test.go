// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubOllama points $OLLAMA_HOST at a server answering /api/tags with body,
// so port discovery finds exactly the models a test declares. The sandbox
// already aims the variable at a dead port, this aims it somewhere alive.
func stubOllama(t *testing.T, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OLLAMA_HOST", srv.URL)
}

// Which weights are loaded is a routing input, not trivia: the same model id
// at Q4_K_M and Q8_0 differs in accuracy, VRAM and tokens/sec, and probe used
// to report both identically because it dropped Ollama's details object (#762).
func TestCLI_Probe_ShowsTheQuantOfALocalHead(t *testing.T) {
	dispatchable(t, "answered")
	stubOllama(t, `{"models":[{"name":"qwen2.5-coder:7b","capabilities":["completion"],
		"details":{"parameter_size":"7.6B","quantization_level":"Q4_K_M"}}]}`)

	out, cobraOut, err := run(t, "probe")
	if err != nil {
		t.Fatalf("`hyctl probe` failed: %v (%s)", err, cobraOut)
	}
	combined := out + cobraOut
	if !strings.Contains(combined, "Quant") {
		t.Errorf("the table has no Quant column though a head reports one:\n%s", combined)
	}
	if !strings.Contains(combined, "Q4_K_M") {
		t.Errorf("probe does not show the quant it discovered:\n%s", combined)
	}
}

// A JSON consumer is the one most likely to route on this, so the value has to
// survive as a field rather than only as table text.
func TestCLI_Probe_JSONCarriesQuantAndParams(t *testing.T) {
	dispatchable(t, "answered")
	stubOllama(t, `{"models":[{"name":"qwen2.5-coder:7b","capabilities":["completion"],
		"details":{"parameter_size":"7.6B","quantization_level":"Q4_K_M"}}]}`)

	out, cobraOut, err := run(t, "probe", "--json")
	if err != nil {
		t.Fatalf("`hyctl probe --json` failed: %v (%s)", err, cobraOut)
	}
	var parsed struct {
		Heads []struct {
			ID     string `json:"id"`
			Quant  string `json:"quant"`
			Params string `json:"params"`
		} `json:"heads"`
	}
	if err := json.Unmarshal([]byte(out+cobraOut), &parsed); err != nil {
		t.Fatalf("probe --json did not parse: %v\n%s", err, out+cobraOut)
	}
	var found bool
	for _, h := range parsed.Heads {
		if h.ID != "ollama/qwen2.5-coder:7b" {
			continue
		}
		found = true
		if h.Quant != "Q4_K_M" || h.Params != "7.6B" {
			t.Errorf("got quant %q params %q, want Q4_K_M / 7.6B", h.Quant, h.Params)
		}
	}
	if !found {
		t.Fatalf("the stubbed local head is missing from --json:\n%s", out+cobraOut)
	}
}

// The column is conditional, and the other branch has to stay honest too: a
// server that reports no quant must not get an empty column, and must not get
// an invented value in --json either.
func TestCLI_Probe_NoQuantMeansNoColumn(t *testing.T) {
	dispatchable(t, "answered")
	stubOllama(t, `{"models":[{"name":"llama3.2:3b","capabilities":["completion"]}]}`)

	out, cobraOut, err := run(t, "probe")
	if err != nil {
		t.Fatalf("`hyctl probe` failed: %v (%s)", err, cobraOut)
	}
	if combined := out + cobraOut; strings.Contains(combined, "Quant") {
		t.Errorf("empty Quant column on a machine where nothing reports one:\n%s", combined)
	}

	jsonOut, jsonCobra, err := run(t, "probe", "--json")
	if err != nil {
		t.Fatalf("`hyctl probe --json` failed: %v (%s)", err, jsonCobra)
	}
	if strings.Contains(jsonOut+jsonCobra, `"quant"`) {
		t.Errorf("--json emitted a quant field for a server that reported none:\n%s", jsonOut+jsonCobra)
	}
}
