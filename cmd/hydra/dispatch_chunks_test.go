// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/checkgen"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/provider"
)

// chunkHead answers each chunk with a value for every field its prompt names, plus extra.
// Call number broken (from 1) answers with no JSON at all.
type chunkHead struct {
	calls  []dispatch.Options
	extra  string
	broken int
}

func (h *chunkHead) dispatch(_ context.Context, prompt string, opts dispatch.Options) (*dispatch.Result, error) {
	h.calls = append(h.calls, opts)
	_, list, _ := strings.Cut(prompt, "inside an object): ")
	var kv []string
	for _, name := range strings.Split(list, ", ") {
		kv = append(kv, fmt.Sprintf("%q: 1", name))
	}
	if h.extra != "" {
		kv = append(kv, h.extra)
	}
	out := "{" + strings.Join(kv, ", ") + "}"
	if len(h.calls) == h.broken {
		out = "sorry, no"
	}
	return &dispatch.Result{Output: out, Head: provider.Head{ID: "h1"}, CostUSD: 0.01,
		Response: &executor.Response{InputTokens: 100, OutputTokens: 10, Duration: time.Second}}, nil
}

func wideSchema(n int) [][]checkgen.Field {
	fields := make([]checkgen.Field, n)
	for i := range fields {
		fields[i] = checkgen.Field{Name: fmt.Sprintf("f%d", i+1), Type: "number"}
	}
	return checkgen.ChunkFields(fields, 30)
}

func TestDispatch_WideSchemaMakesOneCallPerChunk(t *testing.T) {
	h := &chunkHead{}
	res, problems, err := chunkedDispatch(context.Background(), h.dispatch, "Pull the form.", dispatch.Options{}, wideSchema(90))
	if err != nil || len(problems) != 0 {
		t.Fatalf("err = %v, problems = %q", err, problems)
	}
	if len(h.calls) != 3 || h.calls[0].Head != "" || h.calls[1].Head != "h1" || h.calls[2].Head != "h1" {
		t.Fatalf("calls = %+v, want three, the last two pinned to the first's head", h.calls)
	}
	obj, ok := checkgen.DecodeObject(res.Output)
	if !ok || len(obj) != 90 || obj["f90"] == nil {
		t.Fatalf("merged %d fields, want 90:\n%s", len(obj), res.Output)
	}
	if res.InputTokens != 300 || res.Duration != 3*time.Second || res.CostUSD < 0.0299 || res.CostUSD > 0.0301 {
		t.Errorf("usage = %d tokens, %v, $%.4f; want the three chunks summed", res.InputTokens, res.Duration, res.CostUSD)
	}
}

func TestDispatch_ChunkConflictIsReported(t *testing.T) {
	h := &chunkHead{extra: `"note": "x"`}
	_, problems, err := chunkedDispatch(context.Background(), h.dispatch, "Pull the form.", dispatch.Options{}, wideSchema(40))
	if err != nil || len(problems) != 1 || !strings.Contains(problems[0], `"note" came from chunks 1 and 2`) {
		t.Fatalf("err = %v, problems = %q; want the shared key reported", err, problems)
	}
}

// A chunk with no object is reported, and a later conflict still names the right chunks.
func TestDispatch_FailedChunkKeepsChunkNumbers(t *testing.T) {
	h := &chunkHead{extra: `"note": "x"`, broken: 2}
	_, problems, err := chunkedDispatch(context.Background(), h.dispatch, "Pull the form.", dispatch.Options{}, wideSchema(70))
	want := []string{"chunk 2 did not answer with a JSON object", `"note" came from chunks 1 and 3`}
	if err != nil || len(problems) != 2 || problems[0] != want[0] || !strings.Contains(problems[1], want[1]) {
		t.Fatalf("err = %v, problems = %q; want %q", err, problems, want)
	}
}

func TestDispatch_MaxFieldsMustBePositive(t *testing.T) {
	cliSandbox(t)
	if _, _, err := run(t, "dispatch", "--max-fields", "0", "q"); err == nil || !strings.Contains(err.Error(), "--max-fields") {
		t.Fatalf("err = %v, want --max-fields 0 refused", err)
	}
}
