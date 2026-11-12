// SPDX-License-Identifier: MIT

package bench

import (
	"errors"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/pricing"
)

func TestSnapshot_PricesEveryModelOrRefuses(t *testing.T) {
	db := map[string]pricing.ModelPrice{"amazon/nova-micro-v1": {InputPerMillion: 0.035, OutputPerMillion: 0.14}}
	lookup := func(k string) (pricing.ModelPrice, bool) { p, ok := db[k]; return p, ok }
	at := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	over := map[string]Price{"opus": {InPerM: 5, OutPerM: 25, Source: "config:https://x", FetchedAt: at}}
	s, err := Snapshot(lookup, at, []string{"nova", "opus"}, map[string]string{"nova": "amazon/nova-micro-v1"}, over)
	if err != nil {
		t.Fatal(err)
	}
	if s["nova"].Ref != "amazon/nova-micro-v1" || s["nova"].Source != "openrouter" || !s["nova"].FetchedAt.Equal(at) {
		t.Fatalf("nova = %+v", s["nova"])
	}
	if s["opus"].Ref != "override" || s["opus"].Model != "opus" {
		t.Fatalf("opus = %+v", s["opus"])
	}
	if c, _ := s.Cost("opus", 1000, 10); c != (1000*5+10*25)/1e6 {
		t.Fatalf("cost = %v", c)
	}
	if _, err := s.Cost("other", 1, 1); err == nil {
		t.Fatal("priced a model outside the snapshot")
	}
	if _, err := Snapshot(lookup, at, []string{"unknown"}, nil, nil); err == nil {
		t.Fatal("snapshotted a model nothing prices")
	}
	if _, err := Snapshot(lookup, time.Time{}, []string{"nova"}, map[string]string{"nova": "amazon/nova-micro-v1"}, nil); err == nil {
		t.Fatal("snapshotted a price with no fetch date")
	}
}

func TestBudget_ReservesSettlesAndRefuses(t *testing.T) {
	b := NewBudget(1, 0.25)
	if err := b.Reserve(0.5); err != nil {
		t.Fatal(err)
	}
	b.Settle(0.5, 0.1)
	if b.Spent() != 0.35 {
		t.Fatalf("spent %v", b.Spent())
	}
	if err := b.Reserve(0.7); !errors.Is(err, ErrBudget) {
		t.Fatalf("over the cap: %v", err)
	}
	if b.Spent() != 0.35 {
		t.Fatal("a refused reservation was held")
	}
}
