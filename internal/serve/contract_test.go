// SPDX-License-Identifier: MIT

package serve

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// The value a completion reports as its `model` has to be one a client can
// send straight back: SDKs echo it into the next turn and group logs by it.
// Before #1145 it was the head's display name, "Qwen2.5-Coder:7b (Ollama)",
// which /v1/models never advertises and parseRoute cannot resolve, so a real
// client round-tripping its own response got a 502.
func TestChat_ModelFieldIsTheRoutableID(t *testing.T) {
	r := &stubRouter{answer: Answer{
		Output: "pong",
		Head:   "ollama/Qwen2.5-Coder:7b",
		Model:  "Qwen2.5-Coder:7b (Ollama)",
	}}
	w := post(t, r, "", "", `{"model":"hydra/local","messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "ollama/Qwen2.5-Coder:7b" {
		t.Errorf("model = %v, want the routable id ollama/Qwen2.5-Coder:7b", got["model"])
	}

	// And it must survive parseRoute, which is what the round trip really is.
	route, err := parseRoute(fmt.Sprint(got["model"]))
	if err != nil {
		t.Fatalf("the model a response names does not parse as a route: %v", err)
	}
	if route.Head != "ollama/Qwen2.5-Coder:7b" {
		t.Errorf("round trip resolved to %+v, want Head=ollama/Qwen2.5-Coder:7b", route)
	}
}

// A model the client named that cannot work is the client's mistake. 5xx is in
// the OpenAI SDKs' default retry class, so grading these as 502 made a client
// retry something that can never succeed and then report Hydra as broken.
func TestChat_AClientsBadModelIsNotAServerError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"unknown model", fmt.Errorf("%w: no discovered head with id %q", ErrNotFound, "nope"), http.StatusNotFound},
		{"unusable model", fmt.Errorf("%w: embeddings only, never routed", ErrBadRequest), http.StatusBadRequest},
		{"head really failed", fmt.Errorf("upstream returned 500"), http.StatusBadGateway},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, stream := range []bool{false, true} {
				body := `{"model":"x","messages":[{"role":"user","content":"hi"}]}`
				if stream {
					body = `{"model":"x","messages":[{"role":"user","content":"hi"}],"stream":true}`
				}
				w := post(t, &stubRouter{err: tc.err}, "", "", body)
				if w.Code != tc.want {
					t.Errorf("stream=%v: status %d, want %d (%s)", stream, w.Code, tc.want, w.Body.String())
				}
				// The type a client switches on has to agree with the status.
				var got struct {
					Error struct{ Type string } `json:"error"`
				}
				_ = json.Unmarshal(w.Body.Bytes(), &got)
				wantType := "invalid_request_error"
				if tc.want >= 500 {
					wantType = "api_error"
				}
				if got.Error.Type != wantType {
					t.Errorf("stream=%v: error type %q, want %q", stream, got.Error.Type, wantType)
				}
			}
		})
	}
}

// statusFor is the single grading, so the streamed and buffered paths cannot
// come to disagree about what a failure was.
func TestStatusFor_GradesEachKindOnce(t *testing.T) {
	for err, want := range map[error]int{
		ErrNotFound:                      http.StatusNotFound,
		ErrBadRequest:                    http.StatusBadRequest,
		fmt.Errorf("the head timed out"): http.StatusBadGateway,
	} {
		if got := statusFor(err); got != want {
			t.Errorf("statusFor(%v) = %d, want %d", err, got, want)
		}
	}
}
