// SPDX-License-Identifier: MIT

package serve

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ankit373/hydra/internal/executor"
)

// EventKind names what happened while the router worked.
//
// Two, and no success kind: Chat returning an Answer is the success signal.
// This mirrors internal/dispatch's own stream contract, which feeds it, without
// importing dispatch, so this package stays testable with no model.
type EventKind int

const (
	// EventDelta carries output as it arrives.
	EventDelta EventKind = iota
	// EventAttemptFailed fires when a head did not answer and the router moved
	// on to the next. It is why this is an event rather than a bare
	// func(string): a head can stream 200 tokens and then fail, and SSE has no
	// way to take them back.
	EventAttemptFailed
)

// Event is one thing that happened while the router worked.
type Event struct {
	Kind EventKind
	// Text is set on EventDelta only.
	Text string
	// Head and Model identify who produced it, the same two names Answer
	// carries, so a streamed reply names its head the way a buffered one does.
	Head, Model string
	// Reason is set on EventAttemptFailed only.
	Reason string
	// SpanID identifies the attempt, so an abandoned partial stays reachable
	// as `hyctl trace view <run-id> --span <id>`.
	SpanID string
}

// streamer writes one answer as server-sent events.
//
// The response is not started until there is something to write. An error that
// arrives before any frame is then still a real HTTP status: a client that gets
// 200 and an empty stream reads it as the model saying nothing, where a 400
// naming an unknown routing key is something its user can act on.
type streamer struct {
	w     http.ResponseWriter
	id    string
	asked string // the model the client named, until a head is known

	model   string
	open    bool // the role chunk has gone out
	sent    bool // at least one content delta has gone out
	aborted bool
	done    bool // Chat has returned; a late event writes nothing
}

func newStreamer(w http.ResponseWriter, asked string) *streamer {
	return &streamer{w: w, id: "chatcmpl-" + randomID(), asked: asked}
}

// event folds one router event into the stream. cancel stops the dispatch when
// the stream can no longer carry its answer.
func (s *streamer) event(e Event, cancel func()) {
	if s.done || s.aborted {
		return
	}
	switch e.Kind {
	case EventDelta:
		if e.Text == "" {
			return
		}
		s.begin(e.Head, e.Model)
		s.frame(chunk{Delta: delta{Content: e.Text}})
		s.sent = true

	case EventAttemptFailed:
		// A head that failed before emitting anything is invisible: nothing has
		// gone out, so the next head's answer is the only one the client sees.
		// That is the common fallback, and the whole point of one.
		if !s.sent {
			return
		}
		// A head that failed after emitting cannot be taken back. Appending the
		// next head's answer to this partial would compose a reply no head ever
		// gave, so the stream ends here and says why.
		s.aborted = true
		s.fail(fmt.Sprintf("%s stopped after partial output and did not finish: %s. "+
			"What you have is that partial, not an answer%s",
			firstNonEmpty(e.Head, "the head"), e.Reason, spanHint(e.SpanID)))
		cancel()
	}
}

// finish writes the answer's tail: whatever the router did not stream, the tool
// calls, the finish reason, and the usage the client asked for.
func (s *streamer) finish(ans Answer, usage bool) {
	s.done = true
	if s.aborted {
		return
	}
	s.begin(ans.Head, ans.Model)

	// A router that ignored OnEvent still answers correctly: its whole output
	// arrives here, as one delta.
	if !s.sent && ans.Output != "" {
		s.frame(chunk{Delta: delta{Content: ans.Output}})
	}
	if len(ans.ToolCalls) > 0 {
		s.frame(chunk{Delta: delta{ToolCalls: toolFrames(ans.ToolCalls)}})
	}

	reason := finishReason(ans)
	s.frame(chunk{Delta: delta{}, Finish: &reason})
	if usage {
		// The documented shape: a final chunk with an EMPTY choices array whose
		// only payload is usage.
		s.raw(map[string]any{
			"id": s.id, "object": "chat.completion.chunk",
			"created": time.Now().Unix(), "model": s.model,
			"choices": []any{},
			"usage": map[string]int{
				"prompt_tokens":     ans.InputTokens,
				"completion_tokens": ans.OutputTokens,
				"total_tokens":      ans.InputTokens + ans.OutputTokens,
			},
		})
	}
	s.write("data: [DONE]\n\n")
}

// fail ends the stream with an error frame and no [DONE], which means the
// answer completed and this one did not.
func (s *streamer) fail(msg string) {
	s.begin("", "")
	s.raw(map[string]any{"error": map[string]any{"message": msg, "type": "api_error"}})
}

// started reports whether anything has reached the client, which is what
// decides between a real HTTP status and an in-band error frame.
func (s *streamer) started() bool { return s.open }

// begin writes the headers and the role chunk, once. The model is taken from
// the first head that produced anything, so a streamed reply names the head
// that answered rather than the one the client guessed at.
func (s *streamer) begin(head, model string) {
	if s.open {
		return
	}
	s.open = true
	s.model = firstNonEmpty(model, head, s.asked)

	h := s.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	// A buffering proxy would hold every frame until the answer finished,
	// which is the one thing a stream must not do.
	h.Set("X-Accel-Buffering", "no")
	s.w.WriteHeader(http.StatusOK)

	role := "assistant"
	s.frame(chunk{Delta: delta{Role: role}})
}

func (s *streamer) frame(c chunk) {
	c.Index = 0
	s.raw(map[string]any{
		"id": s.id, "object": "chat.completion.chunk",
		"created": time.Now().Unix(), "model": s.model,
		"choices": []chunk{c},
	})
}

func (s *streamer) raw(v any) {
	body, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.write("data: " + string(body) + "\n\n")
}

// write flushes every frame. An unflushed frame is a buffered one, and a stream
// delivered at the end is not a stream.
func (s *streamer) write(str string) {
	_, _ = s.w.Write([]byte(str))
	if fl, ok := s.w.(http.Flusher); ok {
		fl.Flush()
	}
}

// chunk is one `choices` entry of a chat.completion.chunk.
type chunk struct {
	Index  int     `json:"index"`
	Delta  delta   `json:"delta"`
	Finish *string `json:"finish_reason"`
}

type delta struct {
	Role      string      `json:"role,omitempty"`
	Content   string      `json:"content,omitempty"`
	ToolCalls []toolFrame `json:"tool_calls,omitempty"`
}

// toolFrame is a tool call on the wire. Index is always present, unlike
// executor.ToolCall's, because a client reassembles fragments by it and an
// omitted zero is indistinguishable from a missing one.
type toolFrame struct {
	Index    int          `json:"index"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function toolFrameFun `json:"function"`
}

type toolFrameFun struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

// toolFrames sends each call whole rather than split. Hydra has the assembled
// calls by the time it writes them, and a client reassembles a single fragment
// the same way it reassembles twenty.
func toolFrames(calls []executor.ToolCall) []toolFrame {
	out := make([]toolFrame, 0, len(calls))
	for i, c := range calls {
		out = append(out, toolFrame{
			Index: i, ID: c.ID, Type: c.Type,
			Function: toolFrameFun{Name: c.Function.Name, Arguments: c.Function.Arguments},
		})
	}
	return out
}

// spanHint offers the recovery command only when there is a span to recover,
// because a command that opens on nothing is worse than no offer at all.
func spanHint(span string) string {
	if span == "" {
		return ""
	}
	return ", kept as `hyctl trace view --span " + span + "`"
}
