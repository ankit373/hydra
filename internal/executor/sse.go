// SPDX-License-Identifier: MIT

package executor

import (
	"bufio"
	"io"
	"strings"

	"github.com/ankit373/hydra/internal/util"
)

// sseDone is the sentinel the OpenAI shape sends in place of closing the body.
// Anthropic and Gemini just end the response, so its absence is not an error.
const sseDone = "[DONE]"

// scanSSE calls onData with each `data:` payload of a server-sent-event stream.
//
// Shared because the dialects differ in what a payload means, never in how it
// is framed, and a second copy of this loop is where two of them would start
// disagreeing about keep-alives or the read cap.
func scanSSE(r io.Reader, onData func(payload string) error) error {
	sc := bufio.NewScanner(io.LimitReader(r, int64(util.DefaultMaxBytes)+1))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		// Comment keep-alives (":" prefixed) and the blank lines between events
		// are framing, not content. `event:` and `id:` lines say nothing the
		// payload's own type field does not.
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		payload, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == sseDone {
			return nil
		}
		if err := onData(payload); err != nil {
			return err
		}
	}
	return sc.Err()
}
