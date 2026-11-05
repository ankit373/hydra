// SPDX-License-Identifier: MIT

package dispatch

import (
	"slices"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
)

func TestRestrictHeads(t *testing.T) {
	inventory := func() []provider.Head {
		return []provider.Head{
			{ID: "a", Source: "env", Endpoint: "http://a", Provider: "openai"},
			{ID: "b", Source: "env", Endpoint: "http://b", Provider: "openai"},
			{ID: "c", Source: "env", Endpoint: "http://c", Provider: "openai"},
			{ID: "x", Source: "env", Endpoint: "http://x", Provider: "openai",
				Meta: map[string]string{"unroutable_reason": "not in the catalogue"}},
		}
	}
	rows := []struct {
		name    string
		ids     []string
		want    []string
		refused string // what the refusal must name
	}{
		{name: "no list keeps the inventory", ids: nil, want: []string{"a", "b", "c", "x"}},
		{name: "a known subset keeps the discovered order", ids: []string{"c", "a"}, want: []string{"a", "c"}},
		{name: "an unknown id", ids: []string{"a", "nope"}, refused: `"nope"`},
		{name: "a duplicate", ids: []string{"b", "b"}, refused: `"b" is listed twice`},
		{name: "an unroutable head", ids: []string{"x"}, refused: `"x" cannot be routed: not in the catalogue`},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			d := &Dispatcher{heads: inventory()}
			err := d.RestrictHeads(r.ids)
			if r.refused != "" {
				if err == nil || !strings.Contains(err.Error(), r.refused) {
					t.Fatalf("RestrictHeads(%v) = %v, want a refusal naming %s", r.ids, err, r.refused)
				}
				if len(d.heads) != len(inventory()) {
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
				t.Errorf("RestrictHeads(%v) kept %v, want %v", r.ids, got, r.want)
			}
		})
	}
}
