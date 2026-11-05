// SPDX-License-Identifier: MIT

package dispatch

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/provider"
)

func restrictInventory() []provider.Head {
	return []provider.Head{
		{ID: "a", Source: "env", Endpoint: "http://a", Provider: "openai"},
		{ID: "b", Source: "env", Endpoint: "http://b", Provider: "openai"},
		{ID: "l", Source: "port", Endpoint: "http://l", Provider: "local", LocalOnly: true},
		{ID: "e", Source: "port", Endpoint: "http://e", Provider: "local", LocalOnly: true,
			Meta: map[string]string{"embedding_only": "true"}},
		{ID: "x", Source: "env", Endpoint: "http://x", Provider: "openai",
			Meta: map[string]string{"unroutable_reason": "not in the catalogue"}},
	}
}

func TestRestrictHeads(t *testing.T) {
	piiLocal := &config.Config{Policies: map[string]config.Policy{"pii": {Action: "local-only"}}}
	rows := []struct {
		name      string
		ids       []string
		localOnly bool
		cfg       *config.Config
		want      []string
		refused   string // what the refusal must name
	}{
		{name: "no list keeps the inventory", ids: nil, want: []string{"a", "b", "l", "e", "x"}},
		{name: "blank entries are no list", ids: []string{"", " "}, want: []string{"a", "b", "l", "e", "x"}},
		{name: "a known subset keeps the discovered order and the embedder",
			ids: []string{"l", "a"}, want: []string{"a", "l", "e"}},
		{name: "whitespace around an id is trimmed", ids: []string{"a", " b"}, want: []string{"a", "b", "e"}},
		{name: "an embedding head may be listed", ids: []string{"a", "e"}, want: []string{"a", "e"}},
		{name: "a local head under --local", ids: []string{"l", "a"}, localOnly: true, want: []string{"a", "l", "e"}},
		{name: "an unknown id", ids: []string{"a", "nope"}, refused: `"nope"`},
		{name: "a duplicate", ids: []string{"b", " b"}, refused: `"b" is listed twice`},
		{name: "an unroutable head", ids: []string{"x"}, refused: `"x" cannot be routed: not in the catalogue`},
		{name: "only an embedding head", ids: []string{"e"}, refused: "no listed head can be routed"},
		{name: "no local head under --local", ids: []string{"a"}, localOnly: true, refused: "local-only (--local)"},
		{name: "no local head under the pii policy", ids: []string{"a"}, cfg: piiLocal, refused: "the pii policy is local-only"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			cfg := r.cfg
			if cfg == nil {
				cfg = &config.Config{}
			}
			d := &Dispatcher{cfg: cfg, heads: restrictInventory()}
			err := d.RestrictHeads(r.ids, r.localOnly)
			if r.refused != "" {
				if err == nil || !strings.Contains(err.Error(), r.refused) {
					t.Fatalf("RestrictHeads(%q) = %v, want a refusal naming %s", r.ids, err, r.refused)
				}
				if len(d.heads) != len(restrictInventory()) || d.allowed != nil {
					t.Errorf("a refused list still narrowed the inventory to %d heads", len(d.heads))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, h := range d.Heads() {
				got = append(got, h.ID)
			}
			if !slices.Equal(got, r.want) {
				t.Errorf("RestrictHeads(%q) kept %v, want %v", r.ids, got, r.want)
			}
		})
	}
}

// The embedding head nobody listed still feeds the cache's near match and the corpus signals.
func TestRestrictHeads_KeepsTheEmbedder(t *testing.T) {
	d := &Dispatcher{cfg: &config.Config{}, heads: restrictInventory()}
	if err := d.RestrictHeads([]string{"a"}, false); err != nil {
		t.Fatal(err)
	}
	if emb := d.Embedder(); !emb.Available() || emb.Model() != "e" {
		t.Errorf("with a list set the embedder is %T %q, want the discovered model e", emb, emb.Model())
	}
}

// A pin outside the list names the allowlist, not `hyctl probe`: the head may well be on the machine.
func TestPinHead_OutsideTheAllowlistSaysSo(t *testing.T) {
	d := &Dispatcher{cfg: &config.Config{}, heads: restrictInventory()}
	if err := d.RestrictHeads([]string{"a"}, false); err != nil {
		t.Fatal(err)
	}
	_, err := d.pinHead("b", false)
	if !errors.Is(err, ErrHeadUnknown) || !strings.Contains(err.Error(), "--heads` allowlist") {
		t.Errorf("pinning an excluded head = %v, want an unknown-head refusal naming the allowlist", err)
	}
	if _, err := d.pinHead("a", false); err != nil {
		t.Errorf("pinning a listed head = %v", err)
	}
}
