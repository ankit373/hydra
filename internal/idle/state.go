// SPDX-License-Identifier: MIT

// Package idle compacts a conversation while it is idle into typed state that
// points back at the stored originals, and is only used once it passes checks.
package idle

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Turn is one message of a conversation. Ref is the payload hash it is kept
// under and Seq its place in the transcript, from 0.
type Turn struct {
	Role string
	Text string
	Ref  string
	Seq  int
}

// Quote is text taken from a turn, with the ref of the turn it came from.
type Quote struct {
	Text string `json:"text"`
	Ref  string `json:"ref"`
}

// Decision is a choice made in the conversation and the reason given for it.
type Decision struct {
	What string `json:"what"`
	Why  string `json:"why"`
	Ref  string `json:"ref"`
}

// File is a file the conversation touched, filled from tool events, not a model.
type File struct {
	Path   string `json:"path"`
	Action string `json:"action"`
	Ref    string `json:"ref"`
}

// State is a compacted conversation. Constraints are copied byte for byte by
// ExtractConstraints, never written by a model.
type State struct {
	Goal          string     `json:"goal"`
	Constraints   []Quote    `json:"constraints,omitempty"`
	Decisions     []Decision `json:"decisions,omitempty"`
	Facts         []Quote    `json:"facts,omitempty"`
	Files         []File     `json:"files,omitempty"`
	OpenQuestions []Quote    `json:"open_questions,omitempty"`
	Errors        []Quote    `json:"errors,omitempty"`
	Next          []string   `json:"next,omitempty"`
}

// Valid reports the first missing field or ref. Only Goal and Next carry none.
func (s State) Valid() error {
	if strings.TrimSpace(s.Goal) == "" {
		return errors.New("idle: state has no goal")
	}
	for _, f := range []struct {
		name string
		qs   []Quote
	}{
		{"constraint", s.Constraints}, {"fact", s.Facts},
		{"open question", s.OpenQuestions}, {"error", s.Errors},
	} {
		for i, q := range f.qs {
			if q.Text == "" || q.Ref == "" {
				return fmt.Errorf("idle: %s %d needs text and a ref", f.name, i)
			}
		}
	}
	for i, d := range s.Decisions {
		if d.What == "" || d.Why == "" || d.Ref == "" {
			return fmt.Errorf("idle: decision %d needs what, why and a ref", i)
		}
	}
	for i, f := range s.Files {
		if f.Path == "" || f.Action == "" || f.Ref == "" {
			return fmt.Errorf("idle: file %d needs a path, an action and a ref", i)
		}
	}
	for i, n := range s.Next {
		if strings.TrimSpace(n) == "" {
			return fmt.Errorf("idle: next step %d is empty", i)
		}
	}
	return nil
}

// Render is the prompt block for s. Fields go in one fixed order and empty
// ones are left out, so equal states render to equal bytes.
func (s State) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Goal: %s\n", s.Goal)
	quotes := func(title string, qs []Quote) {
		if len(qs) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n%s:\n", title)
		for _, q := range qs {
			fmt.Fprintf(&b, "- %s [ref %s]\n", q.Text, q.Ref)
		}
	}
	quotes("Constraints", s.Constraints)
	if len(s.Decisions) > 0 {
		b.WriteString("\nDecisions:\n")
		for _, d := range s.Decisions {
			fmt.Fprintf(&b, "- %s, because %s [ref %s]\n", d.What, d.Why, d.Ref)
		}
	}
	quotes("Facts", s.Facts)
	if len(s.Files) > 0 {
		b.WriteString("\nFiles:\n")
		for _, f := range s.Files {
			fmt.Fprintf(&b, "- %s %s [ref %s]\n", f.Action, f.Path, f.Ref)
		}
	}
	quotes("Open questions", s.OpenQuestions)
	quotes("Errors", s.Errors)
	if len(s.Next) > 0 {
		b.WriteString("\nNext:\n")
		for _, n := range s.Next {
			fmt.Fprintf(&b, "- %s\n", n)
		}
	}
	return b.String()
}

var (
	ruleWord = regexp.MustCompile(`(?i)\b(must|never|always|do not|don't|don’t|only|make sure)\b`)
	heading  = regexp.MustCompile(`^\s*(?:#{1,6}\s+(.+?)\s*#*|([\p{L} ]+):)\s*$`)
	ruleHead = regexp.MustCompile(`(?i)^(rules|constraints|requirements)$`)
)

// ExtractConstraints copies, byte for byte, every line of a system or user
// turn that states a rule or sits under a rules heading. No model is asked.
// Code fences are skipped: a rule word inside pasted code is not a rule.
func ExtractConstraints(turns []Turn) []Quote {
	var out []Quote
	seen := map[string]bool{}
	for _, t := range turns {
		if t.Role != "system" && t.Role != "user" {
			continue
		}
		underRules, inFence := false, false
		for _, line := range strings.Split(t.Text, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				inFence = !inFence
				continue
			}
			if inFence || strings.TrimSpace(line) == "" {
				continue
			}
			// A heading opens or closes a rules section; one that is itself
			// a rule, like "Always cover these points:", is still kept.
			if m := heading.FindStringSubmatch(line); m != nil {
				if underRules = ruleHead.MatchString(strings.TrimSpace(m[1] + m[2])); underRules {
					continue
				}
			}
			if (underRules || ruleWord.MatchString(line)) && !seen[line] {
				seen[line] = true
				out = append(out, Quote{Text: line, Ref: t.Ref})
			}
		}
	}
	return out
}
