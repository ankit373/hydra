// SPDX-License-Identifier: MIT

// Package entity asks a head whether a text names a person, a place they live
// or an organisation: the personal data no pattern can find.
//
// It speaks no HTTP itself and takes an Ask instead, because internal/policy is
// reachable from internal/evalset and may not egress, and a second detector for
// the same question has to be usable beside the first (#1039).
package entity

import (
	"context"
	"errors"
	"strings"
)

// Ask reaches a head. The caller owns routing, spend and timeouts.
type Ask func(ctx context.Context, system, text string) (string, error)

// System is the instruction the head is given, exported so a measurement and a
// live check cannot ask different questions and compare the answers.
const System = "You label text for personal data. Answer with exactly one word, " +
	"YES or NO. YES if the text names a specific person, a street address, or " +
	"another direct personal identifier. NO otherwise. Source code, commit " +
	"messages, and technical prose that names a person only as the author of a " +
	"result or a library are NO."

// ErrUnreadable reports an answer that was neither yes nor no.
//
// A head that cannot follow a one-word instruction has not reported that the
// text is clean, and reading it as one is how a detector says clean because it
// broke. #1021's rule applied to a model: absent is not negative.
var ErrUnreadable = errors.New("entity: the head answered neither yes nor no")

// ErrNoHead reports that no head was eligible to be asked.
var ErrNoHead = errors.New("entity: no head has measured competence at this")

// Verdict is what one head said about one text.
type Verdict struct {
	Found  bool
	Answer string // what it actually said, so a reader can see the judgement
}

// Check asks one head about one text.
func Check(ctx context.Context, text string, ask Ask) (Verdict, error) {
	if ask == nil {
		return Verdict{}, ErrNoHead
	}
	if strings.TrimSpace(text) == "" {
		return Verdict{}, nil
	}
	raw, err := ask(ctx, System, text)
	if err != nil {
		return Verdict{}, err
	}
	found, ok := parse(raw)
	if !ok {
		return Verdict{Answer: raw}, ErrUnreadable
	}
	return Verdict{Found: found, Answer: raw}, nil
}

// parse reads a one-word answer.
//
// Anchored at the start and requiring a word boundary, because a model that
// explains itself ("No, because...") is answering, while one that buries a word
// mid-sentence is not, and "NO" is a substring of a great many words.
func parse(raw string) (found, ok bool) {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.TrimLeft(s, "*_`\"' \t\n")
	switch {
	case hasWordPrefix(s, "yes"):
		return true, true
	case hasWordPrefix(s, "no"):
		return false, true
	}
	return false, false
}

func hasWordPrefix(s, word string) bool {
	if !strings.HasPrefix(s, word) {
		return false
	}
	if len(s) == len(word) {
		return true
	}
	c := s[len(word)]
	return !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9')
}
