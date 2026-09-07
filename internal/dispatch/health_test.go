// SPDX-License-Identifier: MIT

package dispatch

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/health"
)

func parked(t *testing.T, id, reason string) *health.Store {
	t.Helper()
	s := health.Open(filepath.Join(t.TempDir(), "health.json"))
	s.Fail(id, reason, health.Fatal, time.Now())
	return s
}

// The reported failure: eight heads that had each already proven they could
// not run were selected and dispatched to anyway. A parked head must not be a
// candidate at all, rather than a candidate that is skipped.
func TestSelectHeads_ParkedHeadIsNotACandidate(t *testing.T) {
	d := routingDispatcher()
	d.health = parked(t, "expert", "agy not found")

	for _, h := range d.selectHeads("2", false) {
		if h.ID == "expert" {
			t.Fatal("a parked head was offered as a candidate")
		}
	}
}

func TestSelectHeads_ParkedHeadReturnsOnceItsCooldownElapses(t *testing.T) {
	d := routingDispatcher()
	d.health = parked(t, "expert", "agy not found")

	// selectHeads reads the wall clock, so the elapsed cooldown is expressed by
	// dating the failure far enough back that its retry time has already
	// passed. Calling Pass here would assert nothing but the test's own setup.
	d.health = health.Open(filepath.Join(t.TempDir(), "health.json"))
	d.health.Fail("expert", "agy not found", health.Fatal, time.Now().Add(-24*time.Hour))

	found := false
	for _, h := range d.selectHeads("2", false) {
		if h.ID == "expert" {
			found = true
		}
	}
	if !found {
		t.Error("a recovered head never came back into rotation")
	}
}

// With everything parked the dispatch must say which heads and why, not
// "no available heads", which sends the reader to `hyctl probe` to see a list
// of heads that look fine (#248).
func TestBlockedHeads_NamesParkedHeadsAndTheirReason(t *testing.T) {
	d := routingDispatcher()
	d.health = parked(t, "expert", "agy is not on PATH")

	got := d.blockedHeads(false)
	if !strings.Contains(got, "expert") {
		t.Errorf("blockedHeads = %q, want it to name the parked head", got)
	}
	if !strings.Contains(got, "agy is not on PATH") {
		t.Errorf("blockedHeads = %q, want it to carry the reason", got)
	}
}
