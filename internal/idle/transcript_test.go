// SPDX-License-Identifier: MIT

package idle

import (
	"errors"
	"slices"
	"testing"

	"github.com/ankit373/hydra/internal/payload"
	"github.com/ankit373/hydra/internal/runlog"
)

func openStore(t *testing.T) *payload.Store {
	t.Helper()
	t.Setenv("HYDRA_HOME", t.TempDir())
	s, err := payload.Open(payload.Dir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// stored writes one dispatch's prompt and answer the way capture does.
func stored(t *testing.T, s *payload.Store, system, prompt, answer string) (in, out string) {
	t.Helper()
	in, err := s.PutSegments([]payload.Segment{{Label: "system", Content: system}, {Label: "prompt", Content: prompt}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if out, err = s.PutSegments([]payload.Segment{{Label: "response", Content: answer}}, 1); err != nil {
		t.Fatal(err)
	}
	return in, out
}

func appendEvents(t *testing.T, run string, events ...runlog.Event) {
	t.Helper()
	for _, e := range events {
		if err := runlog.New(run).Append(e); err != nil {
			t.Fatal(err)
		}
	}
}

func roles(ts []Turn) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Role)
	}
	return out
}

func TestTranscript_ConversationFromNewestRun(t *testing.T) {
	s := openStore(t)
	flat := []string{
		"system:\nYou answer billing questions.\n\nuser:\nWhy was I charged twice?",
		"system:\nYou answer billing questions.\n\nuser:\nWhy was I charged twice?\n\nassistant:\nTwo orders.\n\nuser:\nRefund one?",
		"system:\nYou answer billing questions.\n\nuser:\nWhy was I charged twice?\n\nassistant:\nTwo orders.\n\nuser:\nRefund one?\n\nassistant:\nDone.\n\nuser:\nThanks.",
	}
	runs := []string{"20261116T100000Z-a", "20261116T100100Z-b", "20261116T100200Z-c"}
	var in, out string
	for i, run := range runs {
		in, out = stored(t, s, "", flat[i], []string{"Two orders.", "Done.", "Glad to help."}[i])
		appendEvents(t, run,
			runlog.Event{Kind: runlog.KindTaskStarted, Meta: map[string]any{runlog.MetaConversation: "c1"}},
			runlog.Event{Kind: runlog.KindDispatchFinished, InputRef: in, OutputRef: out})
	}
	appendEvents(t, "20261116T100300Z-other",
		runlog.Event{Kind: runlog.KindTaskStarted, Meta: map[string]any{runlog.MetaConversation: "c2"}})

	got, err := Transcript("c1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"system", "user", "assistant", "user", "assistant", "user", "assistant"}
	if r := roles(got.Turns); !slices.Equal(r, want) {
		t.Fatalf("roles %v, want %v", r, want)
	}
	for i, turn := range got.Turns {
		ref := in
		if i == len(got.Turns)-1 {
			ref = out
		}
		if turn.Seq != i || turn.Ref != ref {
			t.Errorf("turn %d: seq %d ref %s, want seq %d ref %s", i, turn.Seq, turn.Ref, i, ref)
		}
	}
	if got.Turns[5].Text != "Thanks." || got.Turns[6].Text != "Glad to help." {
		t.Fatalf("last turns %q, %q", got.Turns[5].Text, got.Turns[6].Text)
	}
}

func TestTranscript_RunTurnsInOrder(t *testing.T) {
	s := openStore(t)
	in1, out1 := stored(t, s, "Write in British English.", "Outline an essay on city trees.", "1. Shade 2. Cost")
	in2, out2 := stored(t, s, "Write in British English.", "Draft the first paragraph.", "Trees cool streets.")
	appendEvents(t, "run-1",
		runlog.Event{Kind: runlog.KindDispatchFinished, InputRef: in1, OutputRef: out1},
		runlog.Event{Kind: runlog.KindDispatchFinished, InputRef: in2, OutputRef: out2})

	got, err := Transcript("run-1")
	if err != nil {
		t.Fatal(err)
	}
	want := []Turn{
		{Role: "system", Text: "Write in British English.", Ref: in1, Seq: 0},
		{Role: "user", Text: "Outline an essay on city trees.", Ref: in1, Seq: 1},
		{Role: "assistant", Text: "1. Shade 2. Cost", Ref: out1, Seq: 2},
		{Role: "user", Text: "Draft the first paragraph.", Ref: in2, Seq: 3},
		{Role: "assistant", Text: "Trees cool streets.", Ref: out2, Seq: 4},
	}
	if !slices.Equal(got.Turns, want) {
		t.Fatalf("got %+v\nwant %+v", got.Turns, want)
	}
}

func TestTranscript_FilesFromEditEventsAndText(t *testing.T) {
	s := openStore(t)
	in, out := stored(t, s, "", "Fix cache_test.go and docs/index.html (see https://x.io/a.html), v3.5, e.g. now.", "Edited internal/cache/cache.go.")
	appendEvents(t, "run-1",
		runlog.Event{Kind: runlog.KindDispatchFinished, InputRef: in, OutputRef: out},
		runlog.Event{Kind: runlog.KindEdit, File: "internal/cache/cache.go", Ref: "edit-ref"})

	got, err := Transcript("run-1")
	if err != nil {
		t.Fatal(err)
	}
	want := []File{
		{Path: "internal/cache/cache.go", Action: FileEdited, Ref: "edit-ref"},
		{Path: "cache_test.go", Action: FileNamed, Ref: in},
		{Path: "docs/index.html", Action: FileNamed, Ref: in},
		{Path: "internal/cache/cache.go", Action: FileNamed, Ref: out},
	}
	if !slices.Equal(got.Files, want) {
		t.Fatalf("got %+v\nwant %+v", got.Files, want)
	}
}

func TestTranscript_CaptureOffIsAnError(t *testing.T) {
	openStore(t)
	appendEvents(t, "run-off", runlog.Event{Kind: runlog.KindDispatchFinished})
	if _, err := Transcript("run-off"); !errors.Is(err, ErrNoOriginals) {
		t.Fatalf("capture off: got %v, want ErrNoOriginals", err)
	}
	appendEvents(t, "run-gone", runlog.Event{Kind: runlog.KindDispatchFinished, InputRef: payload.Hash("evicted")})
	if _, err := Transcript("run-gone"); !errors.Is(err, ErrNoOriginals) {
		t.Fatalf("evicted prompt: got %v, want ErrNoOriginals", err)
	}
	if _, err := Transcript("no-such-chat"); !errors.Is(err, ErrNoOriginals) {
		t.Fatalf("unknown key: got %v, want ErrNoOriginals", err)
	}
}

func TestTranscript_RefusesWhatItCannotRead(t *testing.T) {
	s := openStore(t)
	if _, err := Transcript(""); err == nil {
		t.Fatal("an empty key was accepted")
	}
	appendEvents(t, "run-idle", runlog.Event{Kind: runlog.KindTaskStarted})
	if _, err := Transcript("run-idle"); !errors.Is(err, ErrNoOriginals) {
		t.Fatalf("run with no dispatch: got %v", err)
	}
	appendEvents(t, "20261116T100000Z-x",
		runlog.Event{Kind: runlog.KindTaskStarted, Meta: map[string]any{runlog.MetaConversation: "c-empty"}})
	if _, err := Transcript("c-empty"); !errors.Is(err, ErrNoOriginals) {
		t.Fatalf("conversation with no dispatch: got %v", err)
	}
	in, _ := stored(t, s, "", "hi", "hello")
	appendEvents(t, "run-lost-answer",
		runlog.Event{Kind: runlog.KindDispatchFinished, InputRef: in, OutputRef: payload.Hash("evicted")})
	if _, err := Transcript("run-lost-answer"); !errors.Is(err, ErrNoOriginals) {
		t.Fatalf("evicted answer: got %v", err)
	}
}

// A prompt that was never a flattened chat is one user turn, and a system
// segment stored apart from it comes first.
func TestTranscript_ConversationWithoutRoleLines(t *testing.T) {
	s := openStore(t)
	in, out := stored(t, s, "Be brief.", "Summarise the ticket.", "Card charged twice.")
	appendEvents(t, "20261116T110000Z-a",
		runlog.Event{Kind: runlog.KindTaskStarted, Meta: map[string]any{runlog.MetaConversation: "c3"}},
		runlog.Event{Kind: runlog.KindDispatchFinished, InputRef: in, OutputRef: out})
	got, err := Transcript("c3")
	if err != nil {
		t.Fatal(err)
	}
	if r := roles(got.Turns); !slices.Equal(r, []string{"system", "user", "assistant"}) {
		t.Fatalf("roles %v", r)
	}
}
