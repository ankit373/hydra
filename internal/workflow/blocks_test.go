// SPDX-License-Identifier: MIT

package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/payload"
	"github.com/ankit373/hydra/internal/testutil"
)

func TestBlocks_SameTextStoredOnce(t *testing.T) {
	w := newWorkflow(t, Step{Prompt: "summarise the tickets"})
	a, err := w.Put(BlockDocument, "tickets", "ticket 12: the export button does nothing")
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.Put(BlockDocument, "tickets again", "ticket 12: the export button does nothing")
	if err != nil {
		t.Fatal(err)
	}
	if a != b || len(w.Blocks) != 1 {
		t.Fatalf("same text gave hashes %s and %s and %d blocks, want one", a, b, len(w.Blocks))
	}
	if a != payload.Hash("ticket 12: the export button does nothing") {
		t.Error("the block is not addressed by payload.Hash")
	}
	if blk := w.Blocks[a]; blk.Tokens != len(blk.Text)/4 || blk.Label != "tickets" {
		t.Errorf("block %+v, want the first label and chars/4 tokens", blk)
	}
	if _, err := w.Put("rumour", "x", "y"); err == nil {
		t.Error("an unknown block kind was stored")
	}
}

func TestBlocks_SurviveSaveAndLoad(t *testing.T) {
	testutil.NewSandbox(t)
	w := newWorkflow(t, Step{Prompt: "a"})
	h, err := w.Put(BlockDecision, "tone", "replies are short and name the fix")
	if err != nil {
		t.Fatal(err)
	}
	w.Steps[0].Reads = []string{h}
	if err := Save(w); err != nil {
		t.Fatal(err)
	}
	back, err := Load(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.Blocks[h] != w.Blocks[h] || len(back.Steps[0].Reads) != 1 {
		t.Errorf("after a reload: block %+v and reads %v", back.Blocks[h], back.Steps[0].Reads)
	}
}

func TestBlocks_DeletedWithWorkflow(t *testing.T) {
	testutil.NewSandbox(t)
	w := newWorkflow(t, Step{Prompt: "a"})
	if _, err := w.Put(BlockDocument, "doc", "text"); err != nil {
		t.Fatal(err)
	}
	if err := Save(w); err != nil {
		t.Fatal(err)
	}
	if err := Delete(w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(w.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete, Load = %v, want not found: the blocks live in the record", err)
	}
}

func TestBlocks_OverCapRefused(t *testing.T) {
	w := newWorkflow(t, Step{Prompt: "a"})
	_, err := w.Put(BlockDocument, "dump", strings.Repeat("x", payload.MaxBlobBytes+1))
	if !errors.Is(err, payload.ErrTooLarge) || !strings.Contains(err.Error(), "dump") {
		t.Fatalf("err = %v, want the over-cap refusal naming the block", err)
	}
	if len(w.Blocks) != 0 {
		t.Error("an over-cap block was stored")
	}
}

func TestRun_StepOutputBecomesABlock(t *testing.T) {
	w := newWorkflow(t, Step{Prompt: "extract"}, Step{Prompt: "summarise"})
	r := &funcRouter{fn: func(_ context.Context, prompt string) (StepResult, error) {
		return StepResult{Output: "out of " + own(prompt)}, nil
	}}
	done, err := Run(context.Background(), w, r, nopSave)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range done.Steps {
		blk, ok := done.Blocks[payload.Hash(s.Output)]
		if !ok || blk.Kind != BlockOutput || blk.Label != s.Title {
			t.Errorf("step %d output has block %+v (%v), want an output block", s.N, blk, ok)
		}
	}
}

// A step is given the blocks it reads, fenced, and a step that reads none is not.
func TestStepPrompt_CarriesTheBlocksItReads(t *testing.T) {
	w := newWorkflow(t, Step{Prompt: "summarise", Needs: []int{}}, Step{Prompt: "draft replies", Needs: []int{}})
	h, err := w.Put(BlockDocument, "tickets.csv", "12,export does nothing")
	if err != nil {
		t.Fatal(err)
	}
	w.Steps[0].Reads = []string{h}
	if got := w.stepPrompt(0); !strings.Contains(got, "BEGIN DOCUMENT (tickets.csv)") || !strings.Contains(got, "export does nothing") {
		t.Errorf("step 1 lacks the document it reads:\n%s", got)
	}
	if got := w.stepPrompt(1); got != "draft replies" {
		t.Errorf("step 2 reads nothing but got:\n%s", got)
	}
}

func TestRun_RefusesAReadOfAMissingBlock(t *testing.T) {
	w := newWorkflow(t, Step{Prompt: "a", Reads: []string{"deadbeef"}})
	r := &funcRouter{fn: func(context.Context, string) (StepResult, error) { return StepResult{}, nil }}
	if _, err := Run(context.Background(), w, r, nopSave); err == nil || !strings.Contains(err.Error(), "deadbeef") {
		t.Errorf("err = %v, want the missing block named", err)
	}
	if len(r.asked) != 0 {
		t.Error("a step ran with a block it could not read")
	}
}
