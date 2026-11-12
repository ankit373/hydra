// SPDX-License-Identifier: MIT

package executor

import "strings"

// maxHeldCall bounds the text held back as a possible call. Past it the text
// is released as prose, so a long JSON-shaped answer cannot sit unseen.
const maxHeldCall = 1 << 20

// textCallHold is RecoverToolCalls for a stream: it withholds text only while
// the answer so far could still open a call, and releases it unchanged otherwise.
type textCallHold struct {
	offered []ToolDef
	held    []string
	n       int
	opened  bool // the prefix is `{"` or `[{"`, so only the end can decide
	done    bool // released, or never armed
}

func newTextCallHold(offered []ToolDef) *textCallHold {
	return &textCallHold{offered: offered, done: len(offered) == 0}
}

func (h *textCallHold) write(delta string, sink *deltaSink) {
	if h.done {
		sink.write(delta)
		return
	}
	h.held = append(h.held, delta)
	h.n += len(delta)
	if h.n > maxHeldCall {
		h.release(sink)
		return
	}
	if !h.opened {
		possible, settled := opensCall(strings.Join(h.held, ""))
		if !possible {
			h.release(sink)
			return
		}
		h.opened = settled
	}
}

// structured releases held text once the server reports a call of its own,
// since a server that structures its calls is never second-guessed.
func (h *textCallHold) structured(sink *deltaSink) {
	if !h.done {
		h.release(sink)
	}
}

// finish reads what is still held as a call if the stream completed, and
// otherwise releases it as the text it was.
func (h *textCallHold) finish(sink *deltaSink, complete bool) []ToolCall {
	if h.done {
		return nil
	}
	if complete {
		if calls, ok := RecoverToolCalls(strings.Join(h.held, ""), h.offered); ok {
			h.held, h.done = nil, true
			sink.noteStructured()
			return calls
		}
	}
	h.release(sink)
	return nil
}

// release forwards the held deltas one by one, so the client sees the same
// fragments it would have seen without the hold.
func (h *textCallHold) release(sink *deltaSink) {
	for _, d := range h.held {
		sink.write(d)
	}
	h.held, h.done = nil, true
}

// opensCall reports whether s could begin a call RecoverToolCalls would take,
// and whether that is settled: `{"` or `[{"` past leading whitespace.
func opensCall(s string) (possible, settled bool) {
	s = strings.TrimLeft(s, " \t\r\n")
	if s != "" && s[0] == '[' {
		s = strings.TrimLeft(s[1:], " \t\r\n")
	}
	for _, want := range []byte{'{', '"'} {
		if s == "" {
			return true, false
		}
		if s[0] != want {
			return false, false
		}
		s = strings.TrimLeft(s[1:], " \t\r\n")
	}
	return true, true
}
