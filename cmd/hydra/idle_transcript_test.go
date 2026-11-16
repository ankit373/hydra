// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"slices"
	"testing"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/idle"
)

// A three-turn chat served with capture on reads back as its six turns, so
// the role lines idle splits on are the ones flattenConversation writes.
func TestServe_TranscriptReadsBackServedChat(t *testing.T) {
	dispatchable(t, "answer")
	if err := config.Save(&config.Config{Cortex: "cody", CapturePayloads: true}); err != nil {
		t.Fatal(err)
	}
	d, err := dispatch.New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	reply := executor.Message{Role: "assistant", Content: "answer"}
	asks := []executor.Message{
		{Role: "user", Content: "Summarise the March outage report."},
		{Role: "user", Content: "Which team owned the fix?"},
		{Role: "user", Content: "Draft a two-line status note."},
	}
	runs := []string{"20261116T120000Z-t1", "20261116T120100Z-t2", "20261116T120200Z-t3"}
	var msgs []executor.Message
	for i, ask := range asks {
		if i > 0 {
			msgs = append(msgs, reply)
		}
		msgs = append(msgs, ask)
		chatTurn(t, d, runs[i], "", slices.Clone(msgs)...)
	}

	got, err := idle.Transcript(conversationOf(t, runs[0]))
	if err != nil {
		t.Fatal(err)
	}
	var roles []string
	for _, turn := range got.Turns {
		if turn.Ref == "" {
			t.Fatalf("turn %d has no payload ref", turn.Seq)
		}
		roles = append(roles, turn.Role)
	}
	want := []string{"user", "assistant", "user", "assistant", "user", "assistant"}
	if !slices.Equal(roles, want) {
		t.Fatalf("roles %v, want %v", roles, want)
	}
	if got.Turns[4].Text != asks[2].Content || got.Turns[5].Text != "answer" {
		t.Fatalf("last exchange %q / %q", got.Turns[4].Text, got.Turns[5].Text)
	}
}
