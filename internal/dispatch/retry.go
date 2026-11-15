// SPDX-License-Identifier: MIT

package dispatch

import (
	"fmt"
	"unicode/utf8"

	"github.com/ankit373/hydra/internal/util"
)

// retryAnswerCap bounds the failed answer a retry carries, so a long
// transcript cannot fill the next head's window.
const retryAnswerCap = 8 << 10

// RetryPrompt is the default Options.Retry: the original request, then the
// failed answer and the check's reason, each fenced, and nothing else.
func RetryPrompt(prompt string, failed *Result, reason string) string {
	return prompt + "\n\n" +
		util.WrapUntrusted("REJECTED ANSWER", capMiddle(failed.Output, retryAnswerCap)) + "\n\n" +
		util.WrapUntrusted("CHECK FAILURE", reason) + "\n\n" +
		"A check rejected the answer above for the reason given. Answer the original request again, correctly."
}

// capMiddle keeps the start and end of s within n bytes, since an error and its
// cause tend to sit at either end, and cuts on rune boundaries.
func capMiddle(s string, n int) string {
	if len(s) <= n {
		return s
	}
	head, tail := n/2, len(s)-n/2
	for head > 0 && !utf8.RuneStart(s[head]) {
		head--
	}
	for tail < len(s) && !utf8.RuneStart(s[tail]) {
		tail++
	}
	return s[:head] + fmt.Sprintf("\n[... %d bytes cut ...]\n", tail-head) + s[tail:]
}
