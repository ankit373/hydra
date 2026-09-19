// SPDX-License-Identifier: MIT

package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/ankit373/hydra/internal/executor"
)

// ErrHeadChanged ends a stream whose answer moved to a second head.
//
// The fallback chain and a stream are in direct conflict: a head that fails
// before producing anything is invisible here, but bytes already on the wire
// cannot be retracted, so a second head's output would arrive appended to the
// first one's and read as a single answer.
var ErrHeadChanged = errors.New("the answering head changed after output had already been sent")

// Delta is one piece of a streamed answer, and the head producing it.
//
// The head travels with the text because the client's model field is a routing
// instruction rather than a model name: a chunk echoing "hydra" back would hide
// which head actually answered.
type Delta struct {
	Text  string
	Head  string
	Model string
}

// OnDelta receives output as it arrives, in order and never concurrently with
// itself. A non-nil error means the consumer will accept no more, which the
// router should take as a reason to stop the work rather than finish it.
type OnDelta func(Delta) error

// StreamingRouter is a Router that can deliver an answer incrementally. It must
// not call onDelta after ChatStream has returned.
//
// Optional, the way executor.StreamingExecutor is. A head that cannot stream
// needs no second path here, since executor.Stream delivers its whole output as
// one delta; this interface is about the router, not the head.
type StreamingRouter interface {
	Router
	ChatStream(ctx context.Context, req Request, onDelta OnDelta) (Answer, error)
}

// streamOptions is the client asking for the trailing usage chunk. Sending it
// unasked is what the OpenAI spec says not to do, and a client that sums the
// choices it receives would count it twice.
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type streamDelta struct {
	Role      string              `json:"role,omitempty"`
	Content   string              `json:"content,omitempty"`
	ToolCalls []executor.ToolCall `json:"tool_calls,omitempty"`
}

// streamChoice keeps FinishReason a pointer so a non-final chunk carries an
// explicit null, which is what the wire format specifies and what a client
// branches on to know the answer is still coming.
type streamChoice struct {
	Index        int         `json:"index"`
	Delta        streamDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

// streamer writes one chat.completion.chunk stream, and is the single place
// that decides what a client is allowed to see.
type streamer struct {
	w       http.ResponseWriter
	flush   func()
	id      string
	created int64
	model   string

	// head is the one this stream is committed to, set by the delta that
	// opened it. started records that the status line is gone, so an error
	// after it has to travel inside the stream instead.
	head    string
	started bool
}

// delta forwards one piece of output, opening the stream on the first one.
func (s *streamer) delta(d Delta) error {
	if d.Text == "" {
		return nil
	}
	if !s.started {
		s.head = d.Head
		s.model = firstNonEmpty(d.Model, d.Head, s.model)
		s.open()
		s.chunk(streamChoice{Delta: streamDelta{Role: "assistant", Content: d.Text}})
		return nil
	}
	if d.Head != s.head {
		return fmt.Errorf("%w: %q took over from %q", ErrHeadChanged, d.Head, s.head)
	}
	s.chunk(streamChoice{Delta: streamDelta{Content: d.Text}})
	return nil
}

// finish closes a stream that produced an answer.
func (s *streamer) finish(ans Answer, includeUsage bool) {
	if !s.started {
		// A cache hit runs no head, so it reaches here having emitted nothing.
		// The answer is still the whole answer.
		s.model = firstNonEmpty(ans.Model, ans.Head, s.model)
		s.open()
		s.chunk(streamChoice{Delta: streamDelta{Role: "assistant", Content: ans.Output}})
	}
	if len(ans.ToolCalls) > 0 {
		// The router folds a streamed call before it returns, so calls arrive
		// here complete rather than fragment by fragment. A client accumulates
		// them by index either way.
		s.chunk(streamChoice{Delta: streamDelta{ToolCalls: ans.ToolCalls}})
	}
	reason := finishReason(ans)
	s.chunk(streamChoice{FinishReason: &reason})
	if includeUsage {
		s.event(map[string]any{
			"id": s.id, "object": "chat.completion.chunk", "created": s.created,
			"model":   s.model,
			"choices": []streamChoice{},
			"usage": map[string]int{
				"prompt_tokens":     ans.InputTokens,
				"completion_tokens": ans.OutputTokens,
				"total_tokens":      ans.InputTokens + ans.OutputTokens,
			},
		})
	}
	s.done()
}

// fail ends a stream that has already sent output. The status line is long
// gone, so the error travels in the stream, which is where a client parsing
// SSE will actually see it.
func (s *streamer) fail(err error) {
	s.event(map[string]any{
		"error": map[string]any{"message": err.Error(), "type": errorType(http.StatusBadGateway)},
	})
	s.done()
}

func (s *streamer) open() {
	h := s.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	s.w.WriteHeader(http.StatusOK)
	s.started = true
}

func (s *streamer) chunk(c streamChoice) {
	s.event(map[string]any{
		"id": s.id, "object": "chat.completion.chunk", "created": s.created,
		"model": s.model, "choices": []streamChoice{c},
	})
}

func (s *streamer) event(body any) {
	b, err := json.Marshal(body)
	if err != nil {
		// Only a tool call's arguments can fail here: they are a head's own
		// string, not something this package built. Dropping the chunk would
		// leave the client waiting on an answer it will never be told about.
		s.done()
		return
	}
	_, _ = fmt.Fprintf(s.w, "data: %s\n\n", b)
	s.flush()
}

func (s *streamer) done() {
	_, _ = fmt.Fprint(s.w, "data: [DONE]\n\n")
	s.flush()
}

// streamChat answers one request as SSE.
//
// The response headers are deliberately held back until the first delta: until
// something has been written this is still an ordinary HTTP exchange, so a
// routing mistake or an unreachable head answers with a status the client can
// branch on rather than a 200 carrying an error nobody looks for.
func streamChat(w http.ResponseWriter, req *http.Request, r StreamingRouter, in chatRequest, call Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// With nothing to flush, every chunk waits in the buffer until the
		// handler returns: a whole body wearing a stream's content type.
		writeError(w, http.StatusInternalServerError, "this server cannot stream")
		return
	}

	s := &streamer{
		w: w, flush: flusher.Flush,
		id: "chatcmpl-" + randomID(), created: time.Now().Unix(), model: in.Model,
	}

	// Latched rather than reassigned: a delta that carries no text is accepted
	// by s.delta, so reassigning would let one arriving after the refusal clear
	// it and hand the stream back to a head it had already been taken from.
	var halted error
	ans, err := r.ChatStream(req.Context(), call, func(d Delta) error {
		if halted == nil {
			halted = s.delta(d)
		}
		return halted
	})

	switch {
	case halted != nil:
		s.fail(halted)
	case err != nil && !s.started:
		status := http.StatusBadGateway
		if errors.Is(err, ErrBadRequest) {
			status = http.StatusBadRequest
		}
		writeError(w, status, err.Error())
	case err != nil:
		s.fail(err)
	default:
		s.finish(ans, in.StreamOptions != nil && in.StreamOptions.IncludeUsage)
	}
}
