// SPDX-License-Identifier: MIT

package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/provider"
)

func collect(t *testing.T, recoverable bool, evs ...dispatch.StreamEvent) []ChatStreamEvent {
	t.Helper()
	var got []ChatStreamEvent
	cs := &chatStream{
		runID:       "run-1",
		recoverable: recoverable,
		emit: func(name string, payload any) {
			if name != ChatStreamEventName {
				t.Errorf("emitted on %q, want %q", name, ChatStreamEventName)
			}
			ev, ok := payload.(ChatStreamEvent)
			if !ok {
				t.Fatalf("payload is %T, not a ChatStreamEvent", payload)
			}
			got = append(got, ev)
		},
	}
	for _, ev := range evs {
		cs.on(ev)
	}
	return got
}

func delta(text string) dispatch.StreamEvent {
	return dispatch.StreamEvent{Kind: dispatch.StreamDelta, Text: text, Head: provider.Head{ID: "ollama/qwen3"}}
}

// The offset is what lets a view place a delta it may have missed, so it must
// count the bytes that preceded it and not the events.
func TestChatStream_OffsetCountsBytesBefore(t *testing.T) {
	got := collect(t, false, delta("hello"), delta(" "), delta("world"))

	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}
	for i, want := range []int{0, 5, 6} {
		if got[i].Offset != want {
			t.Errorf("delta %d at offset %d, want %d", i, got[i].Offset, want)
		}
	}
	// Reassembling by offset must give the text back exactly, which is the
	// property the frontend relies on.
	buf := make([]byte, 0, 11)
	for _, ev := range got {
		if ev.Offset != len(buf) {
			t.Fatalf("offset %d does not continue %d bytes", ev.Offset, len(buf))
		}
		buf = append(buf, ev.Text...)
	}
	if string(buf) != "hello world" {
		t.Errorf("reassembled %q", buf)
	}
}

// A multi-byte rune must not be counted as one, or every offset after the
// first non-ASCII character is wrong and the view reports a gap.
func TestChatStream_OffsetIsBytesNotRunes(t *testing.T) {
	got := collect(t, false, delta("héllo"), delta("!"))
	if got[1].Offset != len("héllo") {
		t.Errorf("offset %d after %q, want %d", got[1].Offset, "héllo", len("héllo"))
	}
}

// A fallback replaces the abandoned text rather than continuing it, so the
// next attempt starts from zero. Continuing would make the view append the
// new answer to a partial the head never wrote.
func TestChatStream_ANewAttemptRestartsTheOffset(t *testing.T) {
	got := collect(t, true,
		delta("part"),
		dispatch.StreamEvent{Kind: dispatch.StreamAttemptFailed, Reason: "429 rate limited", SpanID: "abc123"},
		dispatch.StreamEvent{Kind: dispatch.StreamAttemptStarted, Head: provider.Head{ID: "claude"}},
		delta("answer"),
	)

	if len(got) != 4 {
		t.Fatalf("got %d events, want 4: %+v", len(got), got)
	}
	failed := got[1]
	if failed.Kind != "failed" || failed.Reason != "429 rate limited" || failed.SpanID != "abc123" {
		t.Errorf("failure not carried: %+v", failed)
	}
	// It reports where the abandoned partial ended, so the view knows there
	// was one to collapse rather than nothing to show.
	if failed.Offset != 4 {
		t.Errorf("failed at offset %d, want 4", failed.Offset)
	}
	if got[2].Kind != "started" || got[2].Head != "claude" {
		t.Errorf("second attempt not announced: %+v", got[2])
	}
	if got[3].Offset != 0 {
		t.Errorf("the new attempt continued at %d instead of restarting", got[3].Offset)
	}
}

// Without payload capture the span opens but stores nothing, so a view that
// offered it would send someone to an empty page.
func TestChatStream_RecoverableTracksPayloadCapture(t *testing.T) {
	failed := dispatch.StreamEvent{Kind: dispatch.StreamAttemptFailed, Reason: "boom"}
	if got := collect(t, true, failed); !got[0].Recoverable {
		t.Error("payload capture on, but the partial is reported unrecoverable")
	}
	if got := collect(t, false, failed); got[0].Recoverable {
		t.Error("payload capture off, but the partial is offered as readable")
	}
}

// The emitter is nil in tests and before startup. A chat that works must not
// start failing because no webview is listening.
func TestChatStream_NilEmitIsNotAPanic(t *testing.T) {
	cs := &chatStream{runID: "run-1"}
	cs.on(delta("hello"))
	if cs.offset != 5 {
		t.Errorf("offset %d, want the bytes still counted", cs.offset)
	}
}

// A kind this build does not know is a newer dispatch. Emitting it anyway
// would reach the frontend as an event with no kind, which reads as a delta
// carrying no text and quietly resets nothing.
func TestChatStream_UnknownKindIsDropped(t *testing.T) {
	got := collect(t, false, dispatch.StreamEvent{Kind: dispatch.StreamKind(99), Text: "?"})
	if len(got) != 0 {
		t.Errorf("emitted %d events for an unknown kind: %+v", len(got), got)
	}
}

// The event name is a contract between two languages that no compiler checks.
// Renaming either side leaves a chat that silently never streams, with every
// test on both sides still green.
func TestChatStreamEventName_MatchesTheFrontend(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "frontend", "src", "chatStream.ts"))
	if err != nil {
		t.Fatalf("reading the frontend's copy: %v", err)
	}
	// Either quote style: the frontend is hand-formatted with single quotes,
	// and a guard that pinned one would fail on a reformat rather than on the
	// rename it exists to catch.
	single := "CHAT_STREAM_EVENT = '" + ChatStreamEventName + "'"
	double := `CHAT_STREAM_EVENT = "` + ChatStreamEventName + `"`
	if !strings.Contains(string(raw), single) && !strings.Contains(string(raw), double) {
		t.Errorf("chatStream.ts does not declare CHAT_STREAM_EVENT as %q. The Go and "+
			"TypeScript sides must name the same event or nothing arrives.", ChatStreamEventName)
	}
}
