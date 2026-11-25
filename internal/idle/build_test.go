// SPDX-License-Identifier: MIT

package idle

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
)

// stubHead answers note prompts with a fixed note and the state prompt with
// a state that also tries to set constraints, which Build must ignore.
type stubHead struct {
	local  bool
	notes  int
	states int
	cancel func() // called after this many notes, when set
	after  int
}

func (s *stubHead) Head() provider.Head { return provider.Head{ID: "ollama/stub", LocalOnly: s.local} }

func (s *stubHead) Ask(ctx context.Context, prompt string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.HasPrefix(prompt, notePrompt) {
		s.notes++
		if s.cancel != nil && s.notes == s.after {
			s.cancel()
		}
		return fmt.Sprintf("note %d", s.notes), nil
	}
	s.states++
	return "Here it is:\n```json\n" + `{"goal": "file the expense report",
	 "constraints": [{"text": "Submit anything.", "ref": "x"}],
	 "decisions": [{"what": "use the company card", "why": "the user said so", "ref": "r1"}],
	 "facts": [{"text": "trip 4 taxi", "ref": "r4"}],
	 "next": ["attach receipts"]}` + "\n```", nil
}

func transcriptOf(n int) Turns {
	t := agentLoop()
	t.Turns = t.Turns[:n]
	t.Files = []File{{Path: "reports/nov.csv", Action: FileEdited, Ref: "e1"}}
	return t
}

func TestBuild_ConstraintsComeFromCodeNotModel(t *testing.T) {
	turns := transcriptOf(30)
	p, err := Build(context.Background(), &stubHead{local: true}, turns, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.State.Valid(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.State.Constraints, ExtractConstraints(turns.Turns)) {
		t.Fatalf("constraints %v, want the extractor's", p.State.Constraints)
	}
	if !slices.Equal(p.State.Files, turns.Files) {
		t.Fatalf("files %v, want the transcript's", p.State.Files)
	}
	if p.Covers.To != 30 || p.Covers.Hash != CoverHash(turns.Turns) || len(p.Notes) != 30 {
		t.Fatalf("covers %+v with %d notes", p.Covers, len(p.Notes))
	}
}

func TestBuild_IncrementalSummarisesOnlyNewTurns(t *testing.T) {
	head := &stubHead{local: true}
	first, err := Build(context.Background(), head, transcriptOf(30), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	first.State.Facts = append(first.State.Facts, Quote{Text: "trip 7 hotel", Ref: "r7"})
	head.notes = 0
	second, err := Build(context.Background(), head, transcriptOf(35), &first, false)
	if err != nil {
		t.Fatal(err)
	}
	if head.notes != 5 || len(second.Notes) != 35 || second.Covers.To != 35 {
		t.Fatalf("asked %d notes, holds %d, covers %d; want 5, 35, 35", head.notes, len(second.Notes), second.Covers.To)
	}
	// r4 was restated, so the new one wins once; r7 only the old pack had.
	if want := []Quote{{Text: "trip 7 hotel", Ref: "r7"}, {Text: "trip 4 taxi", Ref: "r4"}}; !slices.Equal(second.State.Facts, want) {
		t.Fatalf("facts %v, want %v", second.State.Facts, want)
	}
	if _, err := Build(context.Background(), head, transcriptOf(20), &second, false); err == nil {
		t.Fatal("a transcript shorter than the pack was accepted")
	}
}

func TestBuild_RefusesRemoteHeadWithoutOptIn(t *testing.T) {
	head := &stubHead{local: false}
	if _, err := Build(context.Background(), head, transcriptOf(5), nil, false); !errors.Is(err, ErrRemoteHead) {
		t.Fatalf("got %v, want ErrRemoteHead", err)
	}
	if head.notes+head.states != 0 {
		t.Fatal("a refused remote head was still asked")
	}
	if _, err := Build(context.Background(), head, transcriptOf(5), nil, true); err != nil {
		t.Fatalf("opted in: %v", err)
	}
}

func TestBuild_CancelReturnsNotesOnly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	head := &stubHead{local: true, cancel: cancel, after: 3}
	p, err := Build(ctx, head, transcriptOf(10), nil, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if len(p.Notes) != 3 || p.State.Goal != "" || head.states != 0 {
		t.Fatalf("%d notes, goal %q, %d state asks; want 3 notes and no state", len(p.Notes), p.State.Goal, head.states)
	}
}

func TestBuild_RejectsAReplyThatIsNotState(t *testing.T) {
	for name, h := range map[string]replyHead{
		"prose":       {reply: "I could not summarise this."},
		"no goal":     {reply: `{"next": ["x"]}`},
		"note fails":  {err: errors.New("server down")},
		"state fails": {reply: "note", errOnState: true},
	} {
		if p, err := Build(context.Background(), h, transcriptOf(4), nil, false); err == nil || p.State.Goal != "" {
			t.Errorf("%s: got a pack with state (err %v)", name, err)
		}
	}
}

type replyHead struct {
	reply      string
	err        error
	errOnState bool
}

func (replyHead) Head() provider.Head { return provider.Head{ID: "ollama/bad", LocalOnly: true} }
func (h replyHead) Ask(_ context.Context, prompt string) (string, error) {
	if h.errOnState && strings.HasPrefix(prompt, statePrompt) {
		return "", errors.New("timed out")
	}
	return h.reply, h.err
}
