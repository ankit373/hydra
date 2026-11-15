// SPDX-License-Identifier: MIT

package provider

import "testing"

func TestFamily_LiteLLMUpstreamThenVendorThenProvider(t *testing.T) {
	for h, want := range map[*Head]string{
		{Provider: "openrouter", Meta: map[string]string{"model": "anthropic/claude-sonnet-4.5"}}: "anthropic",
		{Provider: "anthropic", Meta: map[string]string{"model": "claude-sonnet-4.5"}}:            "anthropic",
		{Provider: "local"}: "local",
		{Provider: "litellm", Meta: map[string]string{"model": "claude-sonnet", "litellm_upstream": "anthropic"}}: "anthropic",
		{Provider: "local", Meta: map[string]string{"model": "/models/x.gguf"}}:                                   "local",
	} {
		if got := Family(*h); got != want {
			t.Errorf("Family(%+v) = %q, want %q", *h, got, want)
		}
	}
}
