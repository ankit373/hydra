// SPDX-License-Identifier: MIT

package idle

import (
	"fmt"
	"strings"
	"testing"
)

// supportChat is a support conversation long enough that a pack is smaller.
func supportChat() Turns {
	pad := strings.Repeat("The customer pasted the full invoice history again. ", 30)
	t := Turns{Turns: []Turn{
		{Role: "system", Text: "You are the billing assistant for Acme.\nNever promise a refund over $200 without a manager.", Ref: "s"},
		{Role: "user", Text: "I was charged twice for invoice 4417, 120 dollars each. " + pad, Ref: "u1"},
		{Role: "assistant", Text: "I see two charges of 120 dollars on invoice 4417. " + pad, Ref: "a1"},
		{Role: "user", Text: "Refund one of them please. Only email me.", Ref: "u2"},
		{Role: "assistant", Text: "I will refund one charge, since both are the same invoice. " + pad, Ref: "a2"},
	}}
	for i := range t.Turns {
		t.Turns[i].Seq = i
	}
	return t
}

func goodPack(t Turns) Pack {
	return Pack{
		State: State{
			Goal:        "refund the double charge",
			Constraints: ExtractConstraints(t.Turns),
			Decisions:   []Decision{{What: "refund one charge", Why: "both are the same invoice", Ref: "a2"}},
			Facts:       []Quote{{Text: "invoice 4417 was charged twice", Ref: "u1"}},
			Next:        []string{"email the customer"},
		},
		Notes:  []Note{{Seq: 1, Text: "double charge on invoice 4417", Ref: "u1"}},
		Covers: Covers{From: 0, To: len(t.Turns), Hash: coverHash(t.Turns)},
	}
}

func allStored(string) bool { return true }

func TestVerify_GoodPackPasses(t *testing.T) {
	turns := supportChat()
	v := Verify(goodPack(turns), turns, allStored)
	if !v.Pass || v.Reason != "" || !v.ConstraintsExact || v.TokensAfter >= v.TokensBefore {
		t.Fatalf("verdict %+v, want a pass", v)
	}
}

func TestVerify_RewrittenRuleFails(t *testing.T) {
	turns := supportChat()
	for name, edit := range map[string]func(*State){
		"reworded": func(s *State) { s.Constraints[0].Text = "Refunds over $200 need a manager." },
		"dropped":  func(s *State) { s.Constraints = s.Constraints[1:] },
	} {
		p := goodPack(turns)
		edit(&p.State)
		if v := Verify(p, turns, allStored); v.Pass || v.Reason != "rule rewritten" || v.ConstraintsExact {
			t.Errorf("%s: verdict %+v, want rule rewritten", name, v)
		}
	}
}

func TestVerify_InventedNumberFails(t *testing.T) {
	turns := supportChat()
	p := goodPack(turns)
	p.State.Facts = append(p.State.Facts, Quote{Text: "the account has 1350 users", Ref: "u1"})
	v := Verify(p, turns, allStored)
	if v.Pass || !strings.HasPrefix(v.Reason, "ungrounded: ") || !strings.Contains(v.Reason, "1350") {
		t.Fatalf("verdict %+v, want ungrounded naming 1350", v)
	}
}

func TestVerify_InflatedPackFails(t *testing.T) {
	turns := supportChat()
	p := goodPack(turns)
	p.Notes = append(p.Notes, Note{Seq: 2, Text: strings.Repeat("double charge on invoice 4417 ", 400), Ref: "a1"})
	if v := Verify(p, turns, allStored); v.Pass || v.Reason != "inflated" {
		t.Fatalf("verdict %+v, want inflated", v)
	}
}

func TestVerify_DanglingRefFails(t *testing.T) {
	turns := supportChat()
	p := goodPack(turns)
	p.State.Facts[0].Ref = "gone"
	if v := Verify(p, turns, allStored); v.Pass || v.Reason != "dangling ref: gone" {
		t.Fatalf("verdict %+v, want dangling ref: gone", v)
	}
	// A ref of a covered turn that the store has evicted dangles too.
	evicted := func(ref string) bool { return ref != "u1" }
	if v := Verify(goodPack(turns), turns, evicted); v.Pass || v.Reason != "dangling ref: u1" {
		t.Fatalf("verdict %+v, want dangling ref: u1", v)
	}
}

func TestVerify_ChangedTurnsFail(t *testing.T) {
	turns := supportChat()
	p := goodPack(turns)
	turns.Turns[3].Text = "Refund both."
	if v := Verify(p, turns, allStored); v.Pass || v.Reason != "covers other turns" {
		t.Fatalf("verdict %+v, want covers other turns", v)
	}
}

// Past maxGroundBytes each ref's claims are checked against its own turns,
// and the reason says which turns.
func TestVerify_LargeSpanNamesTheGroup(t *testing.T) {
	turns := supportChat()
	big := strings.Repeat("More invoice history. ", maxGroundBytes/20)
	for i := 0; i < 3; i++ {
		n := len(turns.Turns)
		turns.Turns = append(turns.Turns, Turn{Role: "tool", Text: big, Ref: fmt.Sprintf("t%d", i), Seq: n})
	}
	turns.Files = []File{{Path: "refunds.csv", Action: FileEdited, Ref: "e1"}}
	p := goodPack(turns)
	// A claim citing a file's ref has no turn of its own; it uses the newest turns.
	p.State.Facts = append(p.State.Facts, Quote{Text: "the refund is logged", Ref: "e1"})
	if v := Verify(p, turns, allStored); !v.Pass {
		t.Fatalf("verdict %+v, want a pass per group", v)
	}
	// 4417 is in u1, not in a2, so citing a2 for it fails in a2's turns.
	p.State.Facts = append(p.State.Facts, Quote{Text: "invoice 4417 is closed", Ref: "a2"})
	if v := Verify(p, turns, allStored); v.Pass || v.Reason != "ungrounded in turns 4-4: 4417" {
		t.Fatalf("verdict %+v, want ungrounded in turns 4-4", v)
	}
}
