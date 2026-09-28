// SPDX-License-Identifier: MIT

package evalset

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/util"
)

func addOne(t *testing.T, path string, e Example) Example {
	t.Helper()
	if _, err := Add(path, e); err != nil {
		t.Fatal(err)
	}
	all, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == 0 {
		t.Fatal("nothing was filed")
	}
	return all[len(all)-1]
}

// Half a pair can never be used: a vector with no model cannot be compared to
// anything, and a model with no vector names a space with nothing in it.
func TestAdd_KeepsAVectorOnlyWithItsModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "examples.jsonl")
	vec := util.EncodeVec([]float32{1, 2, 3})

	got := addOne(t, path, Example{Candidate: "a", TaskHash: "t1", Embedding: vec, EmbedModel: "m"})
	if got.Embedding == "" || got.EmbedModel != "m" {
		t.Errorf("a complete pair was not kept: %q/%q", got.Embedding, got.EmbedModel)
	}
	got = addOne(t, path, Example{Candidate: "b", TaskHash: "t2", Embedding: vec})
	if got.Embedding != "" {
		t.Errorf("a vector with no model was kept: %q", got.Embedding)
	}
	got = addOne(t, path, Example{Candidate: "c", TaskHash: "t3", EmbedModel: "m"})
	if got.EmbedModel != "" {
		t.Errorf("a model with no vector was kept: %q", got.EmbedModel)
	}
}

// An upgrade must not rewrite a corpus anyone already has. The fields are
// omitempty so an example without them serialises byte-identically, and its
// dedup key is unchanged, which is what stops every record being re-added.
func TestExample_WithoutAVectorSerialisesAsBefore(t *testing.T) {
	raw, err := json.Marshal(Example{V: SchemaVersion, TaskHash: "t", Candidate: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "embedding") || strings.Contains(string(raw), "embed_model") {
		t.Errorf("an example with no vector names the field anyway: %s", raw)
	}

	path := filepath.Join(t.TempDir(), "examples.jsonl")
	e := Example{Candidate: "c", TaskHash: "t"}
	if added, err := Add(path, e); err != nil || !added {
		t.Fatalf("first add: added=%v err=%v", added, err)
	}
	if added, err := Add(path, e); err != nil || added {
		t.Errorf("the same example was added twice: added=%v err=%v", added, err)
	}
}

// Two embedding models are two corpora. Reporting one total would describe a
// corpus that does not exist, so they are never summed.
func TestTrainable_NeverAggregatesAcrossModels(t *testing.T) {
	v4 := util.EncodeVec([]float32{1, 2, 3, 4})
	tr := Trainable([]Example{
		{Embedding: v4, EmbedModel: "a", Enum: "SIMPLE"},
		{Embedding: v4, EmbedModel: "a", Enum: "COMPLEX"},
		{Embedding: v4, EmbedModel: "b", Enum: "SIMPLE"},
		{Enum: "SIMPLE"}, // no vector at all
	})
	if len(tr) != 2 {
		t.Fatalf("got %d models, want 2: %+v", len(tr), tr)
	}
	if tr[0].Model != "a" || tr[0].Total != 2 || tr[0].Dim != 4 {
		t.Errorf("model a: %+v", tr[0])
	}
	if !tr[0].Separable {
		t.Error("two enums under one model did not read as separable")
	}
	if tr[1].Separable {
		t.Errorf("one enum read as separable: %+v", tr[1])
	}
}

// Cosine between two vector lengths is undefined rather than merely worse, so a
// second length under one model name is reported, never quietly dropped.
func TestTrainable_ReportsMixedDimensions(t *testing.T) {
	tr := Trainable([]Example{
		{Embedding: util.EncodeVec([]float32{1, 2}), EmbedModel: "a", Enum: "SIMPLE"},
		{Embedding: util.EncodeVec([]float32{1, 2, 3}), EmbedModel: "a", Enum: "COMPLEX"},
	})
	if len(tr) != 1 {
		t.Fatalf("got %d models, want 1", len(tr))
	}
	if !tr[0].MixedDims {
		t.Errorf("two vector lengths under one model did not report as mixed: %+v", tr[0])
	}
	if tr[0].Total != 2 {
		t.Errorf("a mismatched vector was dropped instead of reported: %+v", tr[0])
	}
}

// A vector that will not decode is not a vector, so it cannot count toward what
// a classifier could be fitted on.
func TestTrainable_IgnoresAnUnreadableVector(t *testing.T) {
	tr := Trainable([]Example{{Embedding: "not base64 at all!", EmbedModel: "a", Enum: "SIMPLE"}})
	if len(tr) != 0 {
		t.Errorf("an unreadable vector counted as trainable: %+v", tr)
	}
}
