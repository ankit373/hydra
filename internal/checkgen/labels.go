// SPDX-License-Identifier: MIT

package checkgen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/util"
)

const labelsPrompt = "The request below asks for an answer from a fixed set of labels. " +
	"List those labels exactly as the request writes them, as a JSON array of strings. " +
	"If the request names no set, reply [].\n\n"

// labelsGen reads the allowed label set from a classification request.
type labelsGen struct{}

func init() {
	if err := Register("classification", labelsGen{}); err != nil {
		panic(err)
	}
}

func (labelsGen) Kind() string { return "labels" }

// Generate keeps only labels the request itself contains, so a label the writer
// invented never becomes a check. Fewer than two leaves nothing to check.
func (labelsGen) Generate(ctx context.Context, task Task) ([]Spec, error) {
	if task.Ask == nil {
		return nil, errors.New("checkgen labels: no writer head to ask")
	}
	reply, err := task.Ask(ctx, labelsPrompt+util.WrapUntrusted("REQUEST", task.Text))
	if err != nil {
		return nil, err
	}
	var listed []string
	start, end := strings.Index(reply, "["), strings.LastIndex(reply, "]")
	if start < 0 || end < start || json.Unmarshal([]byte(reply[start:end+1]), &listed) != nil {
		return nil, fmt.Errorf("checkgen labels: writer did not reply with a JSON list: %.80q", reply)
	}
	text := oracle.NormaliseLabel(task.Text)
	seen := map[string]bool{}
	var labels []string
	for _, l := range listed {
		n := oracle.NormaliseLabel(l)
		if n == "" || seen[n] || !containsWord(text, n) {
			continue
		}
		seen[n] = true
		labels = append(labels, l)
	}
	if len(labels) < 2 {
		return nil, nil
	}
	s, err := NewSpec("labels", labels, task, task.Writer)
	if err != nil {
		return nil, err
	}
	return []Spec{s}, nil
}

func (labelsGen) Oracle(s Spec) (oracle.Oracle, error) {
	var labels []string
	if err := json.Unmarshal(s.Body, &labels); err != nil {
		return nil, fmt.Errorf("checkgen labels: spec %s: %w", s.ID, err)
	}
	return oracle.NewMatch("gen-labels", labels, "")
}

// containsWord finds w in text with no letter or digit on either side, so "bug" is not found in "debug".
func containsWord(text, w string) bool {
	for i := 0; ; {
		j := strings.Index(text[i:], w)
		if j < 0 {
			return false
		}
		at := i + j
		before, _ := utf8.DecodeLastRuneInString(text[:at])
		after, _ := utf8.DecodeRuneInString(text[at+len(w):])
		if !isWordRune(before) && !isWordRune(after) {
			return true
		}
		i = at + 1
	}
}

func isWordRune(r rune) bool {
	return r != utf8.RuneError && (unicode.IsLetter(r) || unicode.IsDigit(r))
}
