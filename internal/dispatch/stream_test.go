// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/pricing"
	"github.com/ankit373/hydra/internal/provider"
)

// streamingHead answers over SSE, so the dispatch path exercises the real
// streaming executor rather than the one-delta fallback.
func streamingHead(t *testing.T, id string, fragments ...string) provider.Head {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, f := range fragments {
			fmt.Fprintf(w, "data: {\"model\":%q,\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", id, f)
			if fl != nil {
				fl.Flush()
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		if fl != nil {
			fl.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return provider.Head{
		ID: id, Name: id, Provider: "openai", Source: "env",
		Endpoint: srv.URL, AuthReady: true, CapScore: 90,
	}
}

// refusingHead fails after the request is accepted, which is the shape that
// makes attempt boundaries necessary: the chain moves on and a surface must
// know the text it already has is not the answer.
func refusingHead(t *testing.T, id string) provider.Head {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream exploded"}}`))
	}))
	t.Cleanup(srv.Close)
	return provider.Head{
		ID: id, Name: id, Provider: "openai", Source: "env",
		Endpoint: srv.URL, AuthReady: true, CapScore: 95,
	}
}

func streamDispatcher(t *testing.T, heads ...provider.Head) *Dispatcher {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return &Dispatcher{
		cfg: &config.Config{}, heads: heads,
		policy: policy.New(policy.DefaultRules(false)), pricing: pricing.Load(),
	}
}

func TestDispatch_StreamDeltasCarryTheWholeAnswer(t *testing.T) {
	d := streamDispatcher(t, streamingHead(t, "stub", "Hel", "lo ", "there"))

	var events []StreamEvent
	res, err := d.Dispatch(context.Background(), "hi", Options{
		RunID: "run-stream", TaskID: "task-stream",
		OnStream: func(ev StreamEvent) { events = append(events, ev) },
	})
	if err != nil {
		t.Fatal(err)
	}

	var text strings.Builder
	var kinds []StreamKind
	for _, ev := range events {
		kinds = append(kinds, ev.Kind)
		if ev.Kind == StreamDelta {
			text.WriteString(ev.Text)
		}
	}
	if text.String() != res.Output {
		t.Errorf("deltas reassemble to %q, Output is %q: a surface rendering deltas would show a different answer",
			text.String(), res.Output)
	}
	if len(kinds) == 0 || kinds[0] != StreamAttemptStarted {
		t.Errorf("kinds %v: the attempt was not announced before it ran", kinds)
	}
	if events[0].Head.ID != "stub" || events[0].SpanID == "" {
		t.Errorf("start event names head %q span %q, both are needed to attribute the output",
			events[0].Head.ID, events[0].SpanID)
	}
}

// The reason StreamEvent exists rather than a bare func(string).
func TestDispatch_StreamMarksAnAbandonedAttemptBeforeTheNextOneStarts(t *testing.T) {
	d := streamDispatcher(t,
		refusingHead(t, "breaks-first"),
		streamingHead(t, "answers", "the real answer"),
	)

	var events []StreamEvent
	res, err := d.Dispatch(context.Background(), "hi", Options{
		RunID: "run-fallback", TaskID: "task-fallback",
		OnStream: func(ev StreamEvent) { events = append(events, ev) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Head.ID != "answers" {
		t.Fatalf("answered by %q, expected the fallback to be used", res.Head.ID)
	}

	// The failure must be reported before the second head's start, or a
	// surface interleaves the abandoned partial with the real answer.
	var order []string
	for _, ev := range events {
		switch ev.Kind {
		case StreamAttemptStarted:
			order = append(order, "start:"+ev.Head.ID)
		case StreamAttemptFailed:
			order = append(order, "fail:"+ev.Head.ID)
			if ev.Reason == "" {
				t.Error("a failed attempt carries no reason, so nothing can say why it was abandoned")
			}
			if ev.SpanID == "" {
				t.Error("a failed attempt carries no span, so its partial cannot be read back")
			}
		}
	}
	want := []string{"start:breaks-first", "fail:breaks-first", "start:answers"}
	if len(order) != len(want) {
		t.Fatalf("boundary events %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("boundary events %v, want %v", order, want)
		}
	}
	// Every delta must be attributable, and the ones from the head that
	// answered must be the ones that reassemble to Output.
	var text strings.Builder
	for _, ev := range events {
		if ev.Kind == StreamDelta {
			if ev.Head.ID != "answers" {
				t.Errorf("delta attributed to %q, which did not answer", ev.Head.ID)
			}
			text.WriteString(ev.Text)
		}
	}
	if text.String() != res.Output {
		t.Errorf("deltas %q do not reassemble to Output %q", text.String(), res.Output)
	}
}

// The nil path is the one every existing caller takes, and it must not even
// ask the provider to stream: this stub serves SSE only if the request said
// stream:true, so a regression there fails to decode rather than passing
// quietly.
func TestDispatch_NilOnStreamDoesNotRequestAStream(t *testing.T) {
	var sawStream bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(body, &req)
		if req.Stream {
			sawStream = true
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"same answer\"}}]}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, `{"model":"stub","choices":[{"message":{"content":"same answer"}}]}`)
	}))
	t.Cleanup(srv.Close)

	d := streamDispatcher(t, provider.Head{
		ID: "stub", Name: "stub", Provider: "openai", Source: "env",
		Endpoint: srv.URL, AuthReady: true, CapScore: 90,
	})

	quiet, err := d.Dispatch(context.Background(), "hi", Options{
		RunID: "run-quiet", TaskID: "task-quiet",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sawStream {
		t.Error("a dispatch with no OnStream asked the provider to stream")
	}
	if quiet.Output != "same answer" {
		t.Errorf("Output %q with no OnStream", quiet.Output)
	}
}
