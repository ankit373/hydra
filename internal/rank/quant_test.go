// SPDX-License-Identifier: MIT

package rank

import (
	"fmt"
	"slices"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
)

func TestQuantBits_ReadsTheLabelAndAdmitsWhenItCannot(t *testing.T) {
	for _, tc := range []struct {
		label string
		bits  int
		ok    bool
	}{
		// The labels a live Ollama actually reports.
		{"Q4_K_M", 4, true},
		{"Q8_0", 8, true},
		{"F16", 16, true},
		// Other GGUF quants in common circulation.
		{"Q4_0", 4, true},
		{"Q5_K_S", 5, true},
		{"Q6_K", 6, true},
		{"Q2_K", 2, true},
		{"Q3_K_L", 3, true},
		{"IQ2_XXS", 2, true},
		{"IQ4_NL", 4, true},
		{"BF16", 16, true},
		{"F32", 32, true},
		// FP-prefixed microscale formats must not read as F followed by a
		// width: MXFP4 is 4-bit, and "F" then "P4" would parse as nothing.
		{"FP8", 8, true},
		{"NVFP4", 4, true},
		{"MXFP4", 4, true},
		// Case and padding are the server's business, not ours.
		{"q4_k_m", 4, true},
		{"  Q8_0  ", 8, true},
		// Nothing readable. A guess here would pick a head on no evidence.
		{"", 0, false},
		{"unknown", 0, false},
		{"Q", 0, false},
		{"Q_K_M", 0, false},
		{"Q0", 0, false},
	} {
		bits, ok := quantBits(tc.label)
		if bits != tc.bits || ok != tc.ok {
			t.Errorf("quantBits(%q) = (%d, %v), want (%d, %v)", tc.label, bits, ok, tc.bits, tc.ok)
		}
	}
}

// MXFP4 is the case that made the prefix order load-bearing: read "F" first
// and it becomes an unparseable label rather than a 4-bit one.
func TestQuantBits_MicroscaleFormatsAreNotMisreadAsFloats(t *testing.T) {
	for _, label := range []string{"MXFP4", "NVFP4", "MXFP8"} {
		if _, ok := quantBits(label); !ok {
			t.Errorf("quantBits(%q) failed to parse, the FP prefix is being read as F", label)
		}
	}
	if bits, _ := quantBits("MXFP4"); bits != 4 {
		t.Errorf("MXFP4 read as %d bits, want 4", bits)
	}
}

func localHead(id, quant string, score int) provider.Head {
	h := provider.Head{
		ID: id, Name: id, Provider: "local", Source: "port",
		CapScore: score, LocalOnly: true, AuthReady: true,
		Meta: map[string]string{"model_source": "ollama"},
	}
	if quant != "" {
		h.Meta["model_quant"] = quant
	}
	return h
}

// The bug: both quants of one model score the same (ScoreOllama matches on the
// family pattern) and share a source, so score-then-source left them
// incomparable, and an unstable sort picked between them (#765).
func TestByCapScore_PrefersTheBetterQuantOfTheSameModel(t *testing.T) {
	q4 := localHead("ollama/qwen2.5-coder:7b", "Q4_K_M", 66)
	q8 := localHead("ollama/qwen2.5-coder:7b-q8_0", "Q8_0", 66)

	for _, in := range [][]provider.Head{{q4, q8}, {q8, q4}} {
		ranked := ByCapScore(in)
		var firstLocal string
		for _, h := range ranked {
			if h.LocalOnly {
				firstLocal = h.ID
				break
			}
		}
		if firstLocal != q8.ID {
			t.Errorf("input order %s first: ranked %s ahead of the Q8, want %s",
				in[0].ID, firstLocal, q8.ID)
		}
	}
}

// The measured symptom in #765: the winner of the tie flipped as unrelated
// heads were added, crossing the boundary where sort.Slice stops behaving like
// insertion sort. The fix has to hold at every size, so this sweeps it.
func TestByCapScore_TieDoesNotDependOnHowManyOtherHeadsExist(t *testing.T) {
	q4 := localHead("ollama/qwen2.5-coder:7b", "Q4_K_M", 66)
	q8 := localHead("ollama/qwen2.5-coder:7b-q8_0", "Q8_0", 66)

	for _, n := range []int{0, 1, 5, 10, 11, 12, 14, 18, 30, 50} {
		for _, in := range [][]provider.Head{{q4, q8}, {q8, q4}} {
			heads := append([]provider.Head(nil), in...)
			for i := 0; i < n; i++ {
				heads = append(heads, provider.Head{
					ID: fmt.Sprintf("filler-%02d", i), Name: fmt.Sprintf("filler-%02d", i),
					Provider: fmt.Sprintf("vendor-%02d", i), Source: "cli",
					CapScore: 90 - i, AuthReady: true,
					Meta: map[string]string{},
				})
			}
			ranked := ByCapScore(heads)
			var q4Pos, q8Pos = -1, -1
			for i, h := range ranked {
				switch h.ID {
				case q4.ID:
					q4Pos = i
				case q8.ID:
					q8Pos = i
				}
			}
			if q4Pos < 0 || q8Pos < 0 {
				t.Fatalf("n=%d: a quant head vanished from the ranking", n)
			}
			if q8Pos > q4Pos {
				t.Errorf("n=%d input %s first: Q8 at %d ranked behind Q4 at %d",
					n, in[0].ID, q8Pos, q4Pos)
			}
		}
	}
}

// A comparator that is not a total order is what let the sort pick arbitrarily.
// Two distinct heads must never compare as equal in both directions.
func TestByCapScore_OrderIsTotalAndRepeatable(t *testing.T) {
	heads := []provider.Head{
		localHead("ollama/a:7b", "Q4_K_M", 66),
		localHead("ollama/b:7b", "Q4_K_M", 66),
		localHead("ollama/c:7b", "", 66),
		localHead("ollama/d:7b", "", 66),
		localHead("ollama/e:7b", "Q8_0", 66),
	}
	first := ByCapScore(heads)
	// Bits descending, then id, with the two that reported nothing last. The
	// last part is a fixed position rather than a judgement, see
	// unknownQuantBits: what it replaces is an arbitrary position.
	wantOrder := []string{"ollama/e:7b", "ollama/a:7b", "ollama/b:7b", "ollama/c:7b", "ollama/d:7b"}
	for i, want := range wantOrder {
		if first[i].ID != want {
			t.Fatalf("position %d is %s, want %s (full order %v)", i, first[i].ID, want, ids(first))
		}
	}
	// Every permutation of the same set has to rank identically, which is the
	// property "the order is decided by the heads, not by their arrangement".
	for _, perm := range [][]int{{4, 3, 2, 1, 0}, {2, 0, 4, 1, 3}, {1, 4, 0, 3, 2}} {
		shuffled := make([]provider.Head, 0, len(heads))
		for _, i := range perm {
			shuffled = append(shuffled, heads[i])
		}
		got := ByCapScore(shuffled)
		if len(got) != len(first) {
			t.Fatalf("permutation changed the head count: %d vs %d", len(got), len(first))
		}
		for i := range got {
			if got[i].ID != first[i].ID {
				t.Fatalf("permutation %v reordered the result: position %d is %s, want %s",
					perm, i, got[i].ID, first[i].ID)
			}
		}
	}
}

// Nothing may be inferred from a model name: `qwen2.5-coder:7b` is Q4_K_M in
// fact, but the name does not say so, and reading a quant out of a tag would
// be a guess presented as a measurement.
func TestByCapScore_NoQuantIsInferredFromTheName(t *testing.T) {
	named := localHead("ollama/qwen2.5-coder:7b-q8_0", "", 66)
	plain := localHead("ollama/qwen2.5-coder:7b", "", 66)

	if quantRank(named) != unknownQuantBits || quantRank(plain) != unknownQuantBits {
		t.Error("a quant was derived from the model name rather than from what the server reported")
	}
	// With nothing to go on, the order still has to be decided: by ID, so it
	// is at least the same on every machine.
	ranked := ByCapScore([]provider.Head{named, plain})
	if ranked[0].ID != plain.ID {
		t.Errorf("unquantified heads ranked %s first, want the ID order (%s)", ranked[0].ID, plain.ID)
	}
}

// The dedup pass has its own tie, reached only when two heads share a dedupe
// key. Local heads key on ID so they never collide there; a cloud provider's
// entries do, and must not be decided arbitrarily either.
func TestByCapScore_DedupeTieAlsoPrefersTheBetterQuant(t *testing.T) {
	mk := func(id, quant string) provider.Head {
		return provider.Head{
			ID: id, Name: id, Provider: "acme", Source: "env",
			CapScore: 70, AuthReady: true,
			Meta: map[string]string{"model_quant": quant},
		}
	}
	// Same provider, no Meta["model"], so both collapse onto the provider key.
	for _, in := range [][]provider.Head{{mk("acme-lo", "Q4_K_M"), mk("acme-hi", "Q8_0")},
		{mk("acme-hi", "Q8_0"), mk("acme-lo", "Q4_K_M")}} {
		ranked := ByCapScore(in)
		if len(ranked) != 1 {
			t.Fatalf("expected the two to dedupe to one head, got %d", len(ranked))
		}
		if ranked[0].ID != "acme-hi" {
			t.Errorf("dedupe kept %s, want the better-quantized acme-hi", ranked[0].ID)
		}
	}
}

func ids(hs []provider.Head) []string {
	out := make([]string, len(hs))
	for i, h := range hs {
		out[i] = h.ID
	}
	return out
}

// ByCapScore round-trips through a map, and Go randomizes map iteration, so the
// input reaching sort.Slice differs every call. That is what made the old
// comparator a coin flip rather than merely arbitrary: measured on a real head
// set, 40 runs of `hyctl probe` put the Q4 first 23 times and the Q8 17 times.
// One call cannot catch it, so this repeats until randomization would have.
func TestByCapScore_RepeatedCallsOnOneInputAgree(t *testing.T) {
	heads := []provider.Head{
		localHead("ollama/qwen3:0.6b", "Q4_K_M", 63),
		localHead("ollama/qwen3:0.6b-q8_0", "Q8_0", 63),
		localHead("ollama/llama3.2:3b", "Q4_K_M", 60),
		localHead("ollama/mistral:7b", "Q4_K_M", 60),
		localHead("ollama/gemma2:9b", "", 60),
	}
	want := ids(ByCapScore(heads))
	for i := 0; i < 500; i++ {
		if got := ids(ByCapScore(heads)); !slices.Equal(got, want) {
			t.Fatalf("call %d ordered %v, first call ordered %v", i, got, want)
		}
	}
	if want[0] != "ollama/qwen3:0.6b-q8_0" {
		t.Errorf("ranked %s first, want the Q8", want[0])
	}
}
