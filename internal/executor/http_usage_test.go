// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Cost is derived from the token counts, so a provider that answers without a
// usage block used to produce 0 in, 0 out, $0.00, labelled *measured*. Not
// theoretical: the OpenAI-compatible path is what talks to llama.cpp, vLLM and
// LM Studio, and a self-hosted server is free to omit usage (#802).
func TestHTTPResponse_EstimatesWhatTheProviderDidNotReport(t *testing.T) {
	const prompt = "a prompt long enough to estimate from"
	const answer = "an answer long enough to estimate from"

	for _, tc := range []struct {
		name          string
		in, out       int
		wantEstimated bool
		wantInZero    bool
		wantOutZero   bool
	}{
		{name: "both reported", in: 31, out: 64},
		{name: "no usage at all", wantEstimated: true},
		// Per side, so a real count is never thrown away to fix the other one.
		{name: "only the prompt side", in: 31, wantEstimated: true},
		{name: "only the completion side", out: 64, wantEstimated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := Request{Prompt: prompt}
			got := httpResponse(req, answer, "stub-1", tc.in, tc.out, time.Now())

			if got.TokensEstimated != tc.wantEstimated {
				t.Errorf("TokensEstimated = %v, want %v", got.TokensEstimated, tc.wantEstimated)
			}
			if got.InputTokens == 0 || got.OutputTokens == 0 {
				t.Errorf("counts %d/%d: a call that answered must never be logged as free",
					got.InputTokens, got.OutputTokens)
			}
			if tc.in != 0 && got.InputTokens != tc.in {
				t.Errorf("InputTokens = %d, want the reported %d kept", got.InputTokens, tc.in)
			}
			if tc.out != 0 && got.OutputTokens != tc.out {
				t.Errorf("OutputTokens = %d, want the reported %d kept", got.OutputTokens, tc.out)
			}
		})
	}
}

// An empty prompt or an empty answer is genuinely zero tokens, not a missing
// measurement, so estimating one would invent spend rather than recover it.
func TestHTTPResponse_EmptyTextIsNotEstimated(t *testing.T) {
	got := httpResponse(Request{Prompt: ""}, "", "stub-1", 0, 0, time.Now())
	if got.TokensEstimated {
		t.Error("an empty call was labelled estimated; there was nothing to estimate")
	}
	if got.InputTokens != 0 || got.OutputTokens != 0 {
		t.Errorf("counts %d/%d invented from no text", got.InputTokens, got.OutputTokens)
	}
}

// The fallback has to be un-forgettable rather than remembered seven times.
// It was already right on the streamed path and wrong on all seven buffered
// ones, which is the shape of a rule that lives at the call sites.
//
// So: no dialect may build a Response itself. An eighth provider added by
// copying its neighbour inherits the fallback, and one that does not fails
// here rather than silently logging its calls as free.
func TestHTTPResponse_EveryDialectGoesThroughTheHelper(t *testing.T) {
	for _, file := range []string{"http.go", "http_stream.go"} {
		t.Run(file, func(t *testing.T) {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, file, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				id, ok := lit.Type.(*ast.Ident)
				if !ok || id.Name != "Response" {
					return true
				}
				// The helper is the one place allowed to build it.
				if fn := enclosingFunc(f, fset, lit); fn == "httpResponse" {
					return true
				}
				t.Errorf("%s builds a Response directly; use httpResponse so the "+
					"zero-usage fallback cannot be skipped (#802)", fset.Position(lit.Pos()))
				return true
			})
		})
	}
}

func enclosingFunc(f *ast.File, fset *token.FileSet, n ast.Node) string {
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if ok && fn.Pos() <= n.Pos() && n.End() <= fn.End() {
			return fn.Name.Name
		}
	}
	return ""
}

// End to end on the path that actually meets a server which omits usage: a
// local OpenAI-compatible one. The unit test above pins the rule; this pins
// that a real reply reaches it.
func TestExecute_AServerThatReportsNoUsageIsNotFree(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"model":"stub-1","choices":[{"message":{"content":"an answer with real length"}}]}`)
	}))
	defer srv.Close()

	req := Request{Prompt: "a prompt with real length", Head: compatHead(t, srv.URL)}
	got, err := (&HTTPExecutor{client: srv.Client()}).Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got.InputTokens == 0 || got.OutputTokens == 0 {
		t.Errorf("counts %d/%d: the call answered, so it did not cost nothing",
			got.InputTokens, got.OutputTokens)
	}
	if !got.TokensEstimated {
		t.Error("counts Hydra derived are labelled as the provider's own measurement")
	}
	if !strings.Contains(got.Output, "an answer") {
		t.Errorf("output = %q", got.Output)
	}
}
