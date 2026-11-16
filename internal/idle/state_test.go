// SPDX-License-Identifier: MIT

package idle

import (
	"slices"
	"strings"
	"testing"
)

// Three kinds of chat, each with the rules a compactor must keep.
var fixtures = []struct {
	name  string
	turns []Turn
	want  []string
}{
	{
		name: "support",
		turns: []Turn{
			{Ref: "s1", Role: "system", Text: "You are the billing assistant for Acme.\nNever promise a refund over $200 without a manager.\nAlways answer in the customer's language."},
			{Ref: "u1", Role: "user", Text: "I was charged twice on 3 March.\nPlease don't close the ticket until I confirm."},
			{Ref: "a1", Role: "assistant", Text: "I will never close it early, you must confirm first."},
			{Ref: "u2", Role: "user", Text: "Thanks. Only email me, no calls."},
		},
		want: []string{
			"Never promise a refund over $200 without a manager.",
			"Always answer in the customer's language.",
			"Please don't close the ticket until I confirm.",
			"Thanks. Only email me, no calls.",
		},
	},
	{
		name: "writing",
		turns: []Turn{
			{Ref: "u1", Role: "user", Text: "Draft a 600-word essay on city trees.\n\n## Requirements\n\n- British spelling\n- Cite the 2021 council survey\n   - keep quotes under 40 words  \n\n## Background\n\nThe council planted 4,000 trees.\nMake sure the tone stays warm."},
			{Ref: "u2", Role: "user", Text: "Also: do not use the word “lush”."},
		},
		want: []string{
			"- British spelling",
			"- Cite the 2021 council survey",
			"   - keep quotes under 40 words  ",
			"Make sure the tone stays warm.",
			"Also: do not use the word “lush”.",
		},
	},
	{
		name: "code",
		turns: []Turn{
			{Ref: "s1", Role: "system", Text: "Rules:\nTabs for indentation.\nRun go test -race before you finish.\n\nExamples:\nfmt.Println(x)"},
			{Ref: "u1", Role: "user", Text: "Fix the flaky test in cache_test.go.\n```go\n// must never block\nfunc onlyOnce() {}\n```\nYou must not change the public API.\nRules:\nTabs for indentation."},
		},
		want: []string{
			"Tabs for indentation.",
			"Run go test -race before you finish.",
			"You must not change the public API.",
		},
	},
}

func TestExtractConstraints_SupportWritingAndCode(t *testing.T) {
	for _, f := range fixtures {
		var got []string
		for _, q := range ExtractConstraints(f.turns) {
			got = append(got, q.Text)
		}
		if !slices.Equal(got, f.want) {
			t.Errorf("%s:\n got %q\nwant %q", f.name, got, f.want)
		}
	}
}

func TestExtractConstraints_CopiesBytesExactly(t *testing.T) {
	for _, f := range fixtures {
		refs := map[string]string{}
		for _, turn := range f.turns {
			refs[turn.Ref] = turn.Text
		}
		for _, q := range ExtractConstraints(f.turns) {
			src, ok := refs[q.Ref]
			if !ok {
				t.Fatalf("%s: %q points at unknown ref %q", f.name, q.Text, q.Ref)
			}
			if !slices.Contains(strings.Split(src, "\n"), q.Text) {
				t.Errorf("%s: %q is not a line of turn %s byte for byte", f.name, q.Text, q.Ref)
			}
		}
	}
}

func validState() State {
	return State{
		Goal:          "refund the duplicate charge",
		Constraints:   []Quote{{Text: "Never promise a refund over $200.", Ref: "s1"}},
		Decisions:     []Decision{{What: "refund one charge", Why: "both were for order 4411", Ref: "a2"}},
		Facts:         []Quote{{Text: "charged twice on 3 March", Ref: "u1"}},
		Files:         []File{{Path: "tickets/4411.md", Action: "edited", Ref: "t3"}},
		OpenQuestions: []Quote{{Text: "does the card need a reissue?", Ref: "u2"}},
		Errors:        []Quote{{Text: "first refund call returned 409", Ref: "t2"}},
		Next:          []string{"confirm with the customer"},
	}
}

func TestState_InvalidWithoutRefs(t *testing.T) {
	if err := validState().Valid(); err != nil {
		t.Fatalf("a complete state was refused: %v", err)
	}
	for name, breakIt := range map[string]func(*State){
		"goal":          func(s *State) { s.Goal = " " },
		"constraint":    func(s *State) { s.Constraints[0].Ref = "" },
		"decision":      func(s *State) { s.Decisions[0].Ref = "" },
		"decision why":  func(s *State) { s.Decisions[0].Why = "" },
		"fact":          func(s *State) { s.Facts[0].Ref = "" },
		"file":          func(s *State) { s.Files[0].Ref = "" },
		"open question": func(s *State) { s.OpenQuestions[0].Ref = "" },
		"error":         func(s *State) { s.Errors[0].Ref = "" },
		"next":          func(s *State) { s.Next[0] = "" },
	} {
		s := validState()
		breakIt(&s)
		if s.Valid() == nil {
			t.Errorf("a state missing its %s passed Valid", name)
		}
	}
}

func TestState_RenderIsByteStable(t *testing.T) {
	want := `Goal: refund the duplicate charge

Constraints:
- Never promise a refund over $200. [ref s1]

Decisions:
- refund one charge, because both were for order 4411 [ref a2]

Facts:
- charged twice on 3 March [ref u1]

Files:
- edited tickets/4411.md [ref t3]

Open questions:
- does the card need a reissue? [ref u2]

Errors:
- first refund call returned 409 [ref t2]

Next:
- confirm with the customer
`
	for i := 0; i < 3; i++ {
		if got := validState().Render(); got != want {
			t.Fatalf("render %d:\n%s\nwant:\n%s", i, got, want)
		}
	}
	if got := (State{Goal: "g"}).Render(); got != "Goal: g\n" {
		t.Fatalf("empty fields were rendered: %q", got)
	}
}
