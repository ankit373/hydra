// SPDX-License-Identifier: MIT

package serve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/policy"
)

// redactRouter fills the request's vault the way dispatch does for a remote
// head, then streams pieces that split the placeholder.
type redactRouter struct {
	pieces []string
	got    Request
}

func (r *redactRouter) Chat(_ context.Context, req Request) (Answer, error) {
	r.got = req
	policy.RedactReversible("write to sam.lee@example.com", req.Vault)
	for _, p := range r.pieces {
		req.OnEvent(Event{Kind: EventDelta, Text: p, Head: "away"})
	}
	return Answer{Output: "unused once streamed", Head: "away"}, nil
}

func (r *redactRouter) Models() []Model { return nil }

func streamedText(t *testing.T, body string) string {
	t.Helper()
	fs, done := frames(t, body)
	if !done {
		t.Fatalf("stream did not end with [DONE]: %s", body)
	}
	var b strings.Builder
	for _, f := range fs {
		for _, c := range f["choices"].([]any) {
			d := c.(map[string]any)["delta"].(map[string]any)
			if s, ok := d["content"].(string); ok {
				b.WriteString(s)
			}
		}
	}
	return b.String()
}

func TestStream_RestoresPlaceholderSplitAcrossChunks(t *testing.T) {
	r := &redactRouter{pieces: []string{"Sent to <<EMA", "IL_1", ">> just now, a", "nd cc'd <<EMAIL_1>>."}}
	w := stream(t, r, streamTurn)
	got := streamedText(t, w.Body.String())
	want := "Sent to sam.lee@example.com just now, and cc'd sam.lee@example.com."
	if got != want {
		t.Errorf("client read %q, want %q", got, want)
	}
	if strings.Contains(w.Body.String(), "EMA") {
		t.Error("a piece of the placeholder reached the client")
	}
}

func TestStream_HoldsBackAtMostOnePlaceholder(t *testing.T) {
	v := policy.NewVault()
	policy.RedactReversible("write to sam.lee@example.com", v)
	limit := len("<< EMAIL_1 >>")
	rs := v.Stream()
	in := "a long reply with words and spaces and no placeholder in it at all "
	var out strings.Builder
	for i := range len(in) {
		out.WriteString(rs.Push(in[i : i+1]))
		if held := i + 1 - out.Len(); held > limit {
			t.Fatalf("held %d bytes, more than the %d-byte placeholder", held, limit)
		}
	}
	if out.WriteString(rs.Flush()); out.String() != in {
		t.Errorf("streamed %q, want %q", out.String(), in)
	}
}

func TestServe_RedactHeaderOptsIn(t *testing.T) {
	r := &redactRouter{pieces: []string{"Sent to <<EMAIL_1>>."}}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(streamTurn))
	req.Header.Set(RedactHeader, "on")
	w := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	Handler(r, nil, "").ServeHTTP(w, req)

	if !r.got.Redact || r.got.Vault == nil {
		t.Errorf("router got Redact=%v Vault=%v, want the header passed on", r.got.Redact, r.got.Vault)
	}
	if got := w.Header().Get(RedactedHeader); got != "1" {
		t.Errorf("%s = %q, want 1", RedactedHeader, got)
	}

	bad := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(streamTurn))
	bad.Header.Set(RedactHeader, "yes please")
	w = &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	Handler(r, nil, "").ServeHTTP(w, bad)
	if w.Code != http.StatusBadRequest {
		t.Errorf("an unreadable %s answered %d, want 400", RedactHeader, w.Code)
	}
}
