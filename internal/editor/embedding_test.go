// SPDX-License-Identifier: MIT

package editor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/embed"
	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/util"
)

// stubEmbedder records what it was asked to embed, which is what lets a test
// assert the input rather than only that some vector came back.
type stubEmbedder struct {
	model string
	dim   int
	fail  bool
	saw   []string
}

func (s *stubEmbedder) Available() bool { return s.model != "" }
func (s *stubEmbedder) Model() string   { return s.model }

func (s *stubEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	s.saw = append(s.saw, text)
	if s.fail {
		return nil, errors.New("no")
	}
	v := make([]float32, s.dim)
	for i := range v {
		v[i] = float32(len(text)+i) / 100
	}
	return v, nil
}

func corpusAfterRecord(t *testing.T, emb embed.Embedder, v VerifiedEdit) []evalset.Example {
	t.Helper()
	testutil.NewSandbox(t)
	RecordVerifiedEdit(context.Background(), emb, v)
	all, err := evalset.Load(evalset.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	return all
}

var sampleEdit = VerifiedEdit{
	Prompt: "rotate the signing key", File: "a.go", Enum: "MODERATE",
	Head: "cody", Candidate: "package main // CANDIDATE\n", Passed: true,
}

// A vector is only usable alongside the model that produced it, so both are
// stored or neither is.
func TestRecordVerifiedEdit_StoresTheVectorWithItsModel(t *testing.T) {
	emb := &stubEmbedder{model: "nomic-embed-text", dim: 8}
	all := corpusAfterRecord(t, emb, sampleEdit)
	if len(all) != 1 {
		t.Fatalf("filed %d examples, want 1", len(all))
	}
	got := all[0]
	if got.EmbedModel != "nomic-embed-text" {
		t.Errorf("EmbedModel is %q, want the model that produced the vector", got.EmbedModel)
	}
	if n := len(util.DecodeVec(got.Embedding)); n != 8 {
		t.Errorf("the stored vector decodes to %d floats, want 8", n)
	}
}

// The instruction, not the prompt the head saw, and not the candidate. The edit
// prompt is mostly unreviewed file content, so embedding it would put the file
// in the classifier's input instead of the task.
func TestRecordVerifiedEdit_EmbedsTheInstruction(t *testing.T) {
	emb := &stubEmbedder{model: "m", dim: 4}
	corpusAfterRecord(t, emb, sampleEdit)
	if len(emb.saw) != 1 {
		t.Fatalf("embedded %d times, want 1", len(emb.saw))
	}
	if !strings.Contains(emb.saw[0], "rotate the signing key") {
		t.Errorf("embedded %q, want the instruction", emb.saw[0])
	}
	if strings.Contains(emb.saw[0], "CANDIDATE") {
		t.Errorf("embedded the candidate rather than the task: %q", emb.saw[0])
	}
}

// No embedding model is the normal case on most machines, not a failure, so the
// example is filed exactly as it was before this field existed.
func TestRecordVerifiedEdit_NoEmbedderStillFilesTheExample(t *testing.T) {
	// A nil interface, an embedder reporting itself unavailable, and one that
	// fails the call: three ways to have no vector, one filed example each.
	for _, emb := range []embed.Embedder{nil, &stubEmbedder{}, &stubEmbedder{model: "m", dim: 4, fail: true}} {
		all := corpusAfterRecord(t, emb, sampleEdit)
		if len(all) != 1 {
			t.Fatalf("filed %d examples, want 1", len(all))
		}
		if all[0].Embedding != "" || all[0].EmbedModel != "" {
			t.Errorf("an unavailable embedder left %q/%q", all[0].Embedding, all[0].EmbedModel)
		}
		if all[0].Candidate == "" || !all[0].Passed {
			t.Error("the verdict itself was not recorded")
		}
	}
}
