// SPDX-License-Identifier: MIT

package executor

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/util"
)

// reasonRunes caps a one-line failure reason so it fits beside a table row.
const reasonRunes = 120

// CLIError is a CLI head that exited non-zero. Error keeps all of stderr for
// the run log; Reason is the line a person reads.
type CLIError struct {
	Head   string
	Err    error
	Stderr string
}

func (e *CLIError) Error() string {
	return fmt.Sprintf("cli exec %s: %v, stderr: %s", e.Head, e.Err, e.Stderr)
}

func (e *CLIError) Unwrap() error { return e.Err }

// Reason says why a head failed in one line: a CLI head's first stderr line
// with a letter or digit in it, else the error's own, redacted and capped.
func Reason(err error) string {
	line := ""
	var ce *CLIError
	if errors.As(err, &ce) {
		line = meaningfulLine(ce.Stderr)
		if line == "" {
			line = ce.Err.Error()
		}
	} else {
		line = meaningfulLine(err.Error())
	}
	line, _ = policy.Redact(line)
	line = util.SafeTerminal(line)
	if r := []rune(line); len(r) > reasonRunes {
		line = string(r[:reasonRunes]) + "…"
	}
	return line
}

func meaningfulLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); strings.IndexFunc(l, isWordRune) >= 0 {
			return l
		}
	}
	return ""
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
