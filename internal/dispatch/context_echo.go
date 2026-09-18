// SPDX-License-Identifier: MIT

package dispatch

import (
	"strings"

	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/provider"
)

// LLM08:2026 Hidden Context Exposure covers any non-user-facing instruction an
// application assembles. The egress gate handles these on the way in; nothing
// noticed them coming back out, which is the half the category is about.

// classContextEcho marks a finding about hidden context a head disclosed, as
// distinct from a secret it invented (classHeadOutput) or one sent to it.
const classContextEcho = "hidden-context-echo"

// minEchoRunes is the shortest run this will call an echo.
//
// Sliding a window of this size at a stride of the same size means any
// contiguous echo of 2*minEchoRunes-1 or more is guaranteed to align inside one
// window, and shorter ones may or may not be caught. 96 is well past the length
// at which two texts share a run by coincidence, and well under a sentence of
// real instructions.
const minEchoRunes = 96

// maxEchoScan bounds the work on a large response. Beyond it the scan stops,
// which can miss an echo late in a very long answer; a silent quadratic on a
// dispatch path is the worse failure.
const maxEchoScan = 1 << 20

// hiddenContext is one span Hydra assembled that the user never wrote and a
// head should never repeat.
type hiddenContext struct {
	Origin  string // "--system", "a2a handoff"
	Content string
}

// hiddenContextFor names the spans worth checking.
//
// The file content an edit prompt embeds is deliberately excluded: `hyctl edit`
// asks a head to rewrite a file and getting that file back is the answer, so
// including it would file a finding on every successful edit.
func hiddenContextFor(opts Options, handoff string) []hiddenContext {
	var hc []hiddenContext
	if s := strings.TrimSpace(opts.System); s != "" {
		hc = append(hc, hiddenContext{Origin: "--system", Content: s})
	}
	if h := strings.TrimSpace(handoff); h != "" {
		hc = append(hc, hiddenContext{Origin: "a2a handoff", Content: h})
	}

	return hc
}

// normalizeEcho collapses whitespace so a head that reflows the text it repeats
// is still caught. Case is kept: instructions are repeated verbatim or not, and
// folding it would widen the coincidence surface for no gain.
func normalizeEcho(s string) []rune {
	return []rune(strings.Join(strings.Fields(s), " "))
}

// echoedSpans reports which hidden spans appear verbatim in output.
func echoedSpans(output string, hidden []hiddenContext) []string {
	if len(hidden) == 0 {
		return nil
	}
	out := normalizeEcho(output)
	if len(out) < minEchoRunes {
		return nil
	}
	if len(out) > maxEchoScan {
		out = out[:maxEchoScan]
	}
	hay := string(out)

	var found []string
	for _, h := range hidden {
		r := normalizeEcho(h.Content)
		if len(r) < minEchoRunes {
			// Too short to distinguish an echo from a coincidence. Reported as
			// not-an-echo rather than guessed at.
			continue
		}
		if len(r) > maxEchoScan {
			r = r[:maxEchoScan]
		}
		for i := 0; i+minEchoRunes <= len(r); i += minEchoRunes {
			if strings.Contains(hay, string(r[i:i+minEchoRunes])) {
				found = append(found, h.Origin)
				break
			}
		}
	}
	return found
}

// recordContextEcho notes a head that repeated the hidden context back.
//
// Not a gate, for the same reason recordOutputFinding is not: the answer is
// already the caller's and refusing it would lose work over a heuristic. What
// it buys is that "which head disclosed the system instructions" has an answer.
func recordContextEcho(output string, hidden []hiddenContext, h provider.Head) {
	spans := echoedSpans(output, hidden)
	if len(spans) == 0 {
		return
	}
	_ = ledger.Record(ledger.DefaultPath(), ledger.Event{
		Agent: "hydra-dispatch", Tool: h.ID, Action: ledger.Read,
		Resource: "response", Decision: ledger.Allow,
		Classification: classContextEcho, PIITypes: spans,
		Reason: "the response repeats non-user-facing context Hydra assembled (" +
			strings.Join(spans, ", ") + "); it was returned unmodified",
	})
}
