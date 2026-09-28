// SPDX-License-Identifier: MIT

package executor

import (
	"encoding/json"
	"strings"
	"testing"
)

// Every dialect has its own spelling and two bury it inside a config object, so
// a field wired into some of them is the defect this guards: a measurement that
// pins decoding on one head and resamples on another is not a measurement.
func TestTemperature_ReachesEveryDialectBody(t *testing.T) {
	zero := 0.0
	req := Request{Prompt: "hello", MaxTokens: 64, Temperature: &zero}

	bodies := map[string]func() (any, error){
		"openai-compatible": func() (any, error) {
			return openAIChatRequest{MaxTokens: req.MaxTokens, Temp: req.Temperature}, nil
		},
		"gemini":  func() (any, error) { return geminiBody(req) },
		"cohere":  func() (any, error) { return cohereBody(req, "m", false) },
		"bedrock": func() (any, error) { return bedrockBody(req) },
	}
	for name, build := range bodies {
		t.Run(name, func(t *testing.T) {
			b, err := build()
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(b)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), `"temperature":0`) {
				t.Errorf("temperature did not reach the body: %s", raw)
			}
			// Gemini and Bedrock hold both knobs in one object, so setting the
			// second must not drop the first.
			if !strings.Contains(string(raw), "64") {
				t.Errorf("max tokens was lost building temperature in: %s", raw)
			}
		})
	}
}

// nil is the provider's own default, and must put no key on the wire at all: a
// zero sent for "unset" would silently pin every dispatch to greedy decoding.
func TestTemperature_NilSendsNothing(t *testing.T) {
	req := Request{Prompt: "hello", MaxTokens: 64}
	for name, build := range map[string]func() (any, error){
		"openai-compatible": func() (any, error) {
			return openAIChatRequest{MaxTokens: req.MaxTokens, Temp: req.Temperature}, nil
		},
		"gemini":  func() (any, error) { return geminiBody(req) },
		"cohere":  func() (any, error) { return cohereBody(req, "m", false) },
		"bedrock": func() (any, error) { return bedrockBody(req) },
	} {
		t.Run(name, func(t *testing.T) {
			b, _ := build()
			raw, _ := json.Marshal(b)
			if strings.Contains(string(raw), "temperature") {
				t.Errorf("an unset temperature reached the wire: %s", raw)
			}
		})
	}
}
