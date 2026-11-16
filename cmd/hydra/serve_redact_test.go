// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/serve"
	"github.com/ankit373/hydra/internal/testutil"
)

// Through the real router: the hosted stub sees a placeholder, the client reads
// the made-up email back. The stub does not stream; internal/serve covers that.
func TestServe_RedactHeaderEndToEnd(t *testing.T) {
	testutil.NewSandbox(t)
	f := litellmHeads(t, map[string]string{"a": "Sent to <<EMAIL_1>>."}, "summary.general")
	d, err := dispatch.New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	h := serve.Handler(serveRouter{d: d, objective: rank.Cost}, nil, "")

	body := `{"model":"hydra","messages":[{"role":"user","content":"reply to sam.lee@example.com"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set(serve.RedactHeader, "on")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if sent := f.prompt("a"); strings.Contains(sent, "sam.lee@example.com") || !strings.Contains(sent, "<<EMAIL_1>>") {
		t.Errorf("the hosted head was sent %q", sent)
	}
	if !strings.Contains(w.Body.String(), "Sent to sam.lee@example.com.") {
		t.Errorf("the client read %s", w.Body.String())
	}
	if got := w.Header().Get(serve.RedactedHeader); got != "1" {
		t.Errorf("%s = %q, want 1", serve.RedactedHeader, got)
	}
}
