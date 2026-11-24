// SPDX-License-Identifier: MIT

package idle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/payload"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/util"
)

// ErrRemoteHead refuses a head that leaves the machine unless the caller opted in.
var ErrRemoteHead = errors.New("idle: packs are built on a local model unless remote is allowed")

// Head is the model a pack is written by. The scheduler adapts a dispatch
// pinned to the head with LocalOnly and NoCache set.
type Head interface {
	Head() provider.Head
	Ask(ctx context.Context, prompt string) (string, error)
}

// Note is a short summary of one turn, with that turn's ref.
type Note struct {
	Seq  int    `json:"seq"`
	Text string `json:"text"`
	Ref  string `json:"ref"`
}

// Covers is the span of turns a pack stands for, [From, To) by Seq, and a hash
// of exactly those turns, so a swap replaces only that span.
type Covers struct {
	From int    `json:"from"`
	To   int    `json:"to"`
	Hash string `json:"hash"`
}

// Pack is a compacted conversation: state, the notes it was written from, and
// what it covers. Nothing uses it until it passes its checks.
type Pack struct {
	State  State     `json:"state"`
	Notes  []Note    `json:"notes"`
	Covers Covers    `json:"covers"`
	Head   string    `json:"head"`
	Built  time.Time `json:"built"`
}

const notePrompt = "Write a note of at most three short lines on what this turn of a conversation " +
	"says that later work will need: requests, decisions and their reasons, facts, numbers, names. " +
	"Reply with the note only.\n\n"

const statePrompt = "From these notes on a conversation, reply with one JSON object and nothing else:\n" +
	`{"goal": "...", "decisions": [{"what": "...", "why": "...", "ref": "..."}], ` +
	`"facts": [{"text": "...", "ref": "..."}], "open_questions": [{"text": "...", "ref": "..."}], ` +
	`"errors": [{"text": "...", "ref": "..."}], "next": ["..."]}` + "\n" +
	"Every ref must be the ref of the note it came from. Leave out what the notes do not say.\n\n"

// Build summarises the turns since prev into a pack: a note per new turn, then
// state from all notes. Constraints and files come from code, never the model.
// A cancel mid-build returns the notes written so far and no state.
func Build(ctx context.Context, head Head, turns Turns, prev *Pack, allowRemote bool) (Pack, error) {
	if !head.Head().LocalOnly && !allowRemote {
		return Pack{}, fmt.Errorf("%w: %s", ErrRemoteHead, head.Head().ID)
	}
	var notes []Note
	from := 0
	if prev != nil {
		notes = append(notes, prev.Notes...)
		from = prev.Covers.To
	}
	if from > len(turns.Turns) {
		return Pack{}, fmt.Errorf("idle: previous pack covers %d turns, the transcript has %d", from, len(turns.Turns))
	}
	for _, t := range turns.Turns[from:] {
		if err := ctx.Err(); err != nil {
			return Pack{Notes: notes}, err
		}
		text, err := head.Ask(ctx, notePrompt+util.WrapUntrusted("TURN", t.Role+": "+t.Text))
		if err != nil {
			return Pack{Notes: notes}, err
		}
		notes = append(notes, Note{Seq: t.Seq, Text: strings.TrimSpace(text), Ref: t.Ref})
	}
	raw, err := head.Ask(ctx, statePrompt+util.WrapUntrusted("NOTES", renderNotes(notes)))
	if err != nil {
		return Pack{Notes: notes}, err
	}
	var st State
	if err := json.Unmarshal([]byte(jsonObject(raw)), &st); err != nil {
		return Pack{Notes: notes}, fmt.Errorf("idle: the state reply is not JSON: %w", err)
	}
	if prev != nil {
		st.Decisions = mergeByRef(prev.State.Decisions, st.Decisions, func(d Decision) string { return d.Ref })
		st.Facts = mergeByRef(prev.State.Facts, st.Facts, func(q Quote) string { return q.Ref })
	}
	st.Constraints = ExtractConstraints(turns.Turns)
	st.Files = turns.Files
	if err := st.Valid(); err != nil {
		return Pack{Notes: notes}, err
	}
	return Pack{State: st, Notes: notes, Head: head.Head().ID, Built: time.Now().UTC(),
		Covers: Covers{From: 0, To: len(turns.Turns), Hash: CoverHash(turns.Turns)}}, nil
}

func renderNotes(notes []Note) string {
	var b strings.Builder
	for _, n := range notes {
		fmt.Fprintf(&b, "[ref %s] %s\n", n.Ref, n.Text)
	}
	return b.String()
}

// jsonObject is the outermost {...} of a reply, since local models often wrap
// JSON in prose or a code fence.
func jsonObject(s string) string {
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return s
	}
	return s[i : j+1]
}

// mergeByRef keeps prev's entries whose ref the new state does not mention; a
// new entry with the same ref replaces the old one.
func mergeByRef[T any](prev, next []T, ref func(T) string) []T {
	fresh := map[string]bool{}
	for _, x := range next {
		fresh[ref(x)] = true
	}
	out := make([]T, 0, len(prev)+len(next))
	for _, x := range prev {
		if !fresh[ref(x)] {
			out = append(out, x)
		}
	}
	return append(out, next...)
}

// CoverHash names the exact turns a pack covers, by role and text, so a served
// request's messages can be matched against it: they carry no payload refs.
func CoverHash(turns []Turn) string {
	var b strings.Builder
	for _, t := range turns {
		fmt.Fprintf(&b, "%s\x00%s\x00", t.Role, t.Text)
	}
	return payload.Hash(b.String())
}
