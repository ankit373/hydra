// SPDX-License-Identifier: MIT

package idle

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ankit373/hydra/internal/entropy"
	"github.com/ankit373/hydra/internal/ground"
	"github.com/ankit373/hydra/internal/util"
)

// maxGroundBytes is the most original text checked in one ground.Check. Past
// it, each ref's claims are checked against only the turns carrying that ref.
const maxGroundBytes = 256 << 10

// Verdict is what the checks found on a pack. Reason is empty on a pass.
type Verdict struct {
	Pass             bool           `json:"pass"`
	Reason           string         `json:"reason,omitempty"`
	Unsupported      []ground.Claim `json:"unsupported,omitempty"`
	ConstraintsExact bool           `json:"constraints_exact"`
	TokensBefore     int            `json:"tokens_before"`
	TokensAfter      int            `json:"tokens_after"`
	// The probe fields are set by RunProbes. SameHead says the head that
	// answered also wrote the pack, so agreement is weaker evidence.
	Probes     int  `json:"probes,omitempty"`
	ProbeAgree int  `json:"probe_agree,omitempty"`
	Partial    bool `json:"partial,omitempty"`
	SameHead   bool `json:"same_head,omitempty"`
}

// Verify runs the checks that need no model, stopping at the first fail: the
// pack is smaller than the turns it covers, its state is valid and every ref
// resolves (has reports whether the payload store holds a ref), its rules are
// the extractor's byte for byte, and it states nothing the originals do not.
func Verify(p Pack, t Turns, has func(ref string) bool) Verdict {
	if p.Covers.From < 0 || p.Covers.To > len(t.Turns) || p.Covers.From >= p.Covers.To {
		return Verdict{Reason: fmt.Sprintf("covers turns %d-%d of %d", p.Covers.From, p.Covers.To, len(t.Turns))}
	}
	span := t.Turns[p.Covers.From:p.Covers.To]
	if CoverHash(span) != p.Covers.Hash {
		return Verdict{Reason: "covers other turns"}
	}
	text := spanText(span)
	v := Verdict{TokensBefore: entropy.TokenEstimate(text), TokensAfter: entropy.TokenEstimate(p.Text())}
	if v.TokensAfter >= v.TokensBefore {
		v.Reason = "inflated"
		return v
	}
	if err := p.State.Valid(); err != nil {
		v.Reason = err.Error()
		return v
	}
	if ref, ok := danglingRef(p, span, t.Files, has); !ok {
		v.Reason = "dangling ref: " + ref
		return v
	}
	if v.ConstraintsExact = slices.Equal(p.State.Constraints, ExtractConstraints(span)); !v.ConstraintsExact {
		v.Reason = "rule rewritten"
		return v
	}
	if v.Unsupported, v.Reason = groundPack(p, span, len(text)); v.Reason != "" {
		return v
	}
	v.Pass = true
	return v
}

// Blocks are what a swap sends in place of the covered turns: the rules word
// for word, which never change between swaps, and the rest of the state with its notes.
func (p Pack) Blocks() (rules, state string) {
	var b strings.Builder
	for _, q := range p.State.Constraints {
		b.WriteString(q.Text)
		b.WriteByte('\n')
	}
	rest := p.State
	rest.Constraints = nil
	return b.String(), rest.Render() + renderNotes(p.Notes)
}

// Text is both blocks, the size a swap costs.
func (p Pack) Text() string {
	rules, state := p.Blocks()
	return rules + state
}

func spanText(span []Turn) string {
	var b strings.Builder
	for _, t := range span {
		b.WriteString(t.Text)
		b.WriteByte('\n')
	}
	return b.String()
}

// danglingRef returns the first ref that names no covered turn or file, or
// that the payload store no longer holds.
func danglingRef(p Pack, span []Turn, files []File, has func(string) bool) (string, bool) {
	known := map[string]bool{}
	for _, t := range span {
		known[t.Ref] = true
	}
	for _, f := range files {
		known[f.Ref] = true
	}
	for _, r := range p.refs() {
		if !known[r] || !has(r) {
			return r, false
		}
	}
	return "", true
}

// refs lists every ref p points at, in field order.
func (p Pack) refs() []string {
	s := p.State
	var out []string
	for _, qs := range [][]Quote{s.Constraints, s.Facts, s.OpenQuestions, s.Errors} {
		for _, q := range qs {
			out = append(out, q.Ref)
		}
	}
	for _, d := range s.Decisions {
		out = append(out, d.Ref)
	}
	for _, f := range s.Files {
		out = append(out, f.Ref)
	}
	for _, n := range p.Notes {
		out = append(out, n.Ref)
	}
	return out
}

// claim is model-written text and the ref it cites; "" for goal and next steps.
type claim struct{ text, ref string }

// claims is everything in p a model wrote. Constraints and files come from
// code and are checked on their own.
func (p Pack) claims() []claim {
	s := p.State
	out := []claim{{text: s.Goal}}
	for _, qs := range [][]Quote{s.Facts, s.OpenQuestions, s.Errors} {
		for _, q := range qs {
			out = append(out, claim{q.Text, q.Ref})
		}
	}
	for _, d := range s.Decisions {
		out = append(out, claim{d.What + "\n" + d.Why, d.Ref})
	}
	for _, n := range p.Notes {
		out = append(out, claim{n.Text, n.Ref})
	}
	for _, n := range s.Next {
		out = append(out, claim{text: n})
	}
	return out
}

// groundPack checks p's claims against span with ground.Check. A span over
// maxGroundBytes is checked per ref, against the turns carrying it; goal, next
// steps and file refs against the newest turns. The reason names the turns.
func groundPack(p Pack, span []Turn, size int) ([]ground.Claim, string) {
	if size <= maxGroundBytes {
		return groundGroup(p.claims(), span, "")
	}
	turnsOf := map[string][]Turn{}
	for _, t := range span {
		turnsOf[t.Ref] = append(turnsOf[t.Ref], t)
	}
	byRef := map[string][]claim{}
	var order []string
	for _, c := range p.claims() {
		if _, ok := byRef[c.ref]; !ok {
			order = append(order, c.ref)
		}
		byRef[c.ref] = append(byRef[c.ref], c)
	}
	for _, ref := range order {
		group := turnsOf[ref]
		if len(group) == 0 {
			group = tail(span)
		}
		where := fmt.Sprintf(" in turns %d-%d", group[0].Seq, group[len(group)-1].Seq)
		if bad, reason := groundGroup(byRef[ref], group, where); reason != "" {
			return bad, reason
		}
	}
	return nil, ""
}

// tail is the newest turns of span that fit in maxGroundBytes, at least one.
func tail(span []Turn) []Turn {
	i, n := len(span)-1, len(span[len(span)-1].Text)
	for i > 0 && n+len(span[i-1].Text) <= maxGroundBytes {
		i--
		n += len(span[i].Text)
	}
	return span[i:]
}

func groundGroup(cs []claim, group []Turn, where string) ([]ground.Claim, string) {
	texts := make([]string, len(cs))
	for i, c := range cs {
		texts[i] = c.text
	}
	g := ground.Check(strings.Join(texts, "\n"), util.WrapUntrusted("ORIGINAL", spanText(group)))
	if g.Grounded {
		return nil, ""
	}
	bad := make([]string, len(g.Unsupported))
	for i, u := range g.Unsupported {
		bad[i] = u.Text
	}
	return g.Unsupported, "ungrounded" + where + ": " + strings.Join(bad, "; ")
}
