// SPDX-License-Identifier: MIT

package security

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/ledger"
)

func TestDetectConfigDrift_OneConfigurationIsNoDrift(t *testing.T) {
	events := []ledger.Event{
		{TS: "2026-08-01T00:00:00Z", Config: "aaaa1111bbbb2222"},
		{TS: "2026-08-02T00:00:00Z", Config: "aaaa1111bbbb2222"},
	}
	d := DetectConfigDrift(events)
	if d.Changed {
		t.Errorf("Changed = true for a single configuration: %+v", d)
	}
	if len(d.Epochs) != 1 || d.Epochs[0].Events != 2 {
		t.Errorf("Epochs = %+v, want one epoch of 2 events", d.Epochs)
	}
}

// A breadcrumb that changes partway through means the routing/pricing rules
// were swapped mid-history, decisions before and after are not comparable.
func TestDetectConfigDrift_DetectsAMidHistorySwap(t *testing.T) {
	events := []ledger.Event{
		{TS: "2026-08-01T00:00:00Z", Config: "old0000000000000"},
		{TS: "2026-08-02T00:00:00Z", Config: "old0000000000000"},
		{TS: "2026-08-03T00:00:00Z", Config: "new1111111111111"},
	}
	d := DetectConfigDrift(events)
	if !d.Changed || len(d.Epochs) != 2 {
		t.Fatalf("drift = %+v, want two epochs", d)
	}
	// Oldest first, so the reader can see which rules came before.
	if d.Epochs[0].FirstTS != "2026-08-01T00:00:00Z" || d.Epochs[1].FirstTS != "2026-08-03T00:00:00Z" {
		t.Errorf("epochs are not chronological: %+v", d.Epochs)
	}
	if d.Epochs[0].Events != 2 || d.Epochs[1].Events != 1 {
		t.Errorf("event counts = %d/%d, want 2/1", d.Epochs[0].Events, d.Epochs[1].Events)
	}
}

// An unstamped event is "unknown configuration", which is not a configuration
// , folding it into an epoch would invent a span that never existed.
func TestDetectConfigDrift_UnstampedEventsAreCountedSeparately(t *testing.T) {
	events := []ledger.Event{
		{TS: "2026-08-01T00:00:00Z"}, // pre-fingerprint
		{TS: "2026-08-02T00:00:00Z", Config: "aaaa1111bbbb2222"},
	}
	d := DetectConfigDrift(events)
	if d.Unstamped != 1 {
		t.Errorf("Unstamped = %d, want 1", d.Unstamped)
	}
	if d.Changed || len(d.Epochs) != 1 {
		t.Errorf("an unstamped event created a phantom epoch: %+v", d)
	}
}

func TestDriftCheck_Wording(t *testing.T) {
	none := driftCheck(ConfigDrift{})
	if none.Status != "no stamped events" {
		t.Errorf("Status = %q for an unstamped log", none.Status)
	}

	changed := driftCheck(ConfigDrift{
		Changed: true,
		Epochs: []ConfigEpoch{
			{Breadcrumb: "old0", FirstTS: "2026-08-01T00:00:00Z", Events: 2},
			{Breadcrumb: "new1", FirstTS: "2026-08-03T00:00:00Z", Events: 1},
		},
	})
	if !strings.Contains(changed.Status, "2 configurations") {
		t.Errorf("Status = %q, want the configuration count", changed.Status)
	}
	if !strings.Contains(changed.Detail, "2026-08-03") {
		t.Errorf("Detail = %q, want it to name when the rules changed", changed.Detail)
	}
}

// The breadcrumb is only ever compared, never re-derived, so display trims it.
func TestShortBreadcrumb(t *testing.T) {
	if got := shortBreadcrumb("0123456789abcdef0123"); got != "0123456789ab" {
		t.Errorf("shortBreadcrumb = %q", got)
	}
	if got := shortBreadcrumb("short"); got != "short" {
		t.Errorf("a short value was mangled: %q", got)
	}
}
