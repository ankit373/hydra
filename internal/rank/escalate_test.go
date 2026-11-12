// SPDX-License-Identifier: MIT

package rank

import (
	"testing"

	"github.com/ankit373/hydra/internal/provider"
)

func TestEscalation(t *testing.T) {
	failed := provider.Head{ID: "failed", Provider: "openai"}
	same := provider.Head{ID: "same", Provider: "openai"}
	other := provider.Head{ID: "other", Provider: "openrouter", Meta: map[string]string{"model": "anthropic/x"}}
	measured := func(eff, n int) Score { return Score{Effective: eff, InDomain: n} }
	for _, tc := range []struct {
		name   string
		scores map[string]Score
		want   string // "" when nothing may be asked
	}{
		{"the weakest head that beats it", map[string]Score{"failed": measured(90, 100), "same": measured(92, 100), "other": measured(99, 1000)}, "same"},
		{"a tie goes to a family not asked", map[string]Score{"failed": measured(90, 100), "same": measured(92, 100), "other": measured(93, 100)}, "other"},
		{"none measured better", map[string]Score{"failed": measured(95, 100), "same": measured(92, 100), "other": measured(95, 100)}, ""},
		{"a declared score is not a measurement", map[string]Score{"failed": measured(90, 100), "same": {Effective: 99, InDomain: MinCommitments - 1}}, ""},
		{"nothing measured at all", nil, ""},
		{"an unmeasured failure's declared score is no bar", map[string]Score{"failed": {Effective: 99}, "same": measured(92, 100)}, "same"},
		{"an equal step goes to the cheaper", map[string]Score{"failed": measured(90, 100), "same": {Effective: 95, InDomain: 100, CostUSD: 0.03}, "other": {Effective: 95, InDomain: 100, CostUSD: 0.01}}, "other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			heads := []provider.Head{same, other}
			i, ok := Escalation(heads, tc.scores, failed, []provider.Head{failed})
			got := ""
			if ok {
				got = heads[i].ID
			}
			if got != tc.want {
				t.Errorf("Escalation picked %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFamily(t *testing.T) {
	for h, want := range map[*provider.Head]string{
		{Provider: "openrouter", Meta: map[string]string{"model": "anthropic/claude-sonnet-4.5"}}: "anthropic",
		{Provider: "anthropic", Meta: map[string]string{"model": "claude-sonnet-4.5"}}:            "anthropic",
		{Provider: "local"}: "local",
		{Provider: "litellm", Meta: map[string]string{"model": "claude-sonnet", "litellm_upstream": "anthropic"}}: "anthropic",
		{Provider: "local", Meta: map[string]string{"model": "/models/x.gguf"}}:                                   "local",
	} {
		if got := family(*h); got != want {
			t.Errorf("family(%+v) = %q, want %q", *h, got, want)
		}
	}
}
