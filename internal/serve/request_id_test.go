// SPDX-License-Identifier: MIT

package serve

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// chatOnce sends one chat completion and returns the response.
func chatOnce(t *testing.T, r Router, body string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	w := httptest.NewRecorder()
	Handler(r, "").ServeHTTP(w, req)
	return w.Result()
}

// A client that got a bad answer has to be able to find the trace for it. The
// header is the convention every gateway already sets; the body's id is for a
// client that logs only the body. They have to be one value, or a client that
// kept the wrong one cannot be traced (#1149).
func TestChat_ReturnsOneIdentifierInBothTheHeaderAndTheBody(t *testing.T) {
	r := &streamRouter{answer: Answer{Output: "hi", Head: "ollama/a"}}

	res := chatOnce(t, r, `{"model":"hydra","messages":[{"role":"user","content":"q"}]}`)
	header := res.Header.Get(RequestIDHeader)
	if header == "" {
		t.Fatal("no " + RequestIDHeader + " header, so a client cannot thread its request to a trace")
	}

	var got map[string]any
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != header {
		t.Errorf("body id %v and header %q disagree; a client keeping either one must resolve",
			got["id"], header)
	}
	// The router must receive it, or the run it opens is keyed on something
	// the client was never told.
	if r.got.RequestID != header {
		t.Errorf("the router was given %q and the client was told %q", r.got.RequestID, header)
	}
}

// Two requests are two units of work. One id for both is a session, which is
// what made a long-lived server one ever-growing run.
func TestChat_EachRequestGetsItsOwnIdentifier(t *testing.T) {
	r := &streamRouter{answer: Answer{Output: "hi"}}
	body := `{"model":"hydra","messages":[{"role":"user","content":"q"}]}`

	first := chatOnce(t, r, body).Header.Get(RequestIDHeader)
	second := chatOnce(t, r, body).Header.Get(RequestIDHeader)

	if first == "" || first == second {
		t.Errorf("two requests share the identifier %q, so neither can be traced apart", first)
	}
}

// The client that most needs the trace is the one that got an error, so the
// header is written before the router is even called.
func TestChat_AnErrorResponseStillCarriesTheIdentifier(t *testing.T) {
	r := &streamRouter{err: ErrBadRequest}

	res := chatOnce(t, r, `{"model":"hydra","messages":[{"role":"user","content":"q"}]}`)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
	if res.Header.Get(RequestIDHeader) == "" {
		t.Error("a failed request carries no identifier, which is the case that needs one most")
	}
}

// SSE cannot take back a header once a frame has gone out, so the identifier
// has to be set before the first chunk and has to be the one the frames carry.
func TestStream_CarriesTheIdentifierInTheHeaderAndEveryFrame(t *testing.T) {
	r := &streamRouter{
		events: []Event{{Kind: EventDelta, Text: "hel", Head: "ollama/a"}},
		answer: Answer{Output: "hello", Head: "ollama/a"},
	}

	res := chatOnce(t, r, `{"model":"hydra","stream":true,"messages":[{"role":"user","content":"q"}]}`)
	id := res.Header.Get(RequestIDHeader)
	if id == "" {
		t.Fatal("the streamed path set no " + RequestIDHeader)
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok || payload == "[DONE]" {
			continue
		}
		var frame map[string]any
		if json.Unmarshal([]byte(payload), &frame) != nil {
			continue
		}
		if fid, ok := frame["id"]; ok && fid != id {
			t.Fatalf("a frame carries id %v against the header's %q", fid, id)
		}
	}
}
