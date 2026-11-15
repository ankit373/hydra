// SPDX-License-Identifier: MIT

package checkgen

import (
	"testing"

	"github.com/ankit373/hydra/internal/egress"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
)

var (
	gptBig   = provider.Head{ID: "openai/gpt-big", Provider: "openai"}
	gptSmall = provider.Head{ID: "openai/gpt-small", Provider: "openai"}
	claudeOR = provider.Head{ID: "or/claude", Provider: "openrouter", Meta: map[string]string{"model": "anthropic/claude"}}
	geminiG  = provider.Head{ID: "google/gemini", Provider: "google"}
	qwenLoc  = provider.Head{ID: "ollama/qwen", Provider: "local", LocalOnly: true}
	userText = []egress.Part{{Content: "summarise this", Source: egress.SourceUser}}
)

func measuredAt(cost float64) rank.Score {
	return rank.Score{Effective: 90, InDomain: rank.MinCommitments, CostUSD: cost}
}

func pickID(t *testing.T, heads []provider.Head, scores map[string]rank.Score, parts []egress.Part, local bool) (string, bool) {
	t.Helper()
	h, same, ok := PickGenerator(heads, scores, gptBig, parts, local)
	if !ok {
		return "", same
	}
	return h.ID, same
}

func TestPickGenerator_PrefersAnotherFamily(t *testing.T) {
	heads := []provider.Head{gptBig, gptSmall, claudeOR, geminiG}
	scores := map[string]rank.Score{
		gptSmall.ID: measuredAt(0.001), // cheapest overall, but the answer's family
		claudeOR.ID: measuredAt(0.02),
		geminiG.ID:  {CostUSD: 0.005}, // cheaper, but never measured
	}
	if id, same := pickID(t, heads, scores, userText, false); id != claudeOR.ID || same {
		t.Errorf("picked %q (same=%v), want the cheapest measured other family %q", id, same, claudeOR.ID)
	}
	scores[claudeOR.ID] = rank.Score{CostUSD: 0.02}
	if id, same := pickID(t, heads, scores, userText, false); id != geminiG.ID || same {
		t.Errorf("with none measured picked %q (same=%v), want the cheapest other family %q", id, same, geminiG.ID)
	}
}

func TestPickGenerator_FlagsSameFamilyWhenNoChoice(t *testing.T) {
	scores := map[string]rank.Score{gptBig.ID: measuredAt(0.0001), gptSmall.ID: measuredAt(0.01)}
	if id, same := pickID(t, []provider.Head{gptBig, gptSmall}, scores, userText, false); id != gptSmall.ID || !same {
		t.Errorf("picked %q (same=%v), want %q flagged same family, never the answering head first", id, same, gptSmall.ID)
	}
	if id, same := pickID(t, []provider.Head{gptBig}, scores, userText, false); id != gptBig.ID || !same {
		t.Errorf("alone picked %q (same=%v), want the answering head flagged", id, same)
	}
	if _, _, ok := PickGenerator(nil, nil, gptBig, userText, false); ok {
		t.Error("picked a head from none")
	}
}

func TestPickGenerator_KeepsLocalOnlyPromptsLocal(t *testing.T) {
	heads := []provider.Head{gptBig, claudeOR, qwenLoc}
	scores := map[string]rank.Score{claudeOR.ID: measuredAt(0.001), qwenLoc.ID: {CostUSD: 0.01}}
	if id, _ := pickID(t, heads, scores, userText, true); id != qwenLoc.ID {
		t.Errorf("--local prompt picked %q, want the local head", id)
	}
	secret := []egress.Part{{Content: "AKIA...", Source: egress.SourceUser, Sens: egress.Secret}}
	if id, _ := pickID(t, heads, scores, secret, false); id != qwenLoc.ID {
		t.Errorf("secret prompt picked %q, want the local head", id)
	}
	if id, _ := pickID(t, heads[:2], scores, secret, false); id != "" {
		t.Errorf("secret prompt with no local head picked %q, want none", id)
	}
}
