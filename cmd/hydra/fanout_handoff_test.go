// SPDX-License-Identifier: MIT

package main

import (
	"testing"

	"github.com/ankit373/hydra/internal/a2a"
	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/swarm"
	"github.com/ankit373/hydra/internal/testutil"

	"path/filepath"
)

func fanoutAttempt(id string, status swarm.HeadStatus) swarm.Attempt {
	return swarm.Attempt{Head: provider.Head{ID: id, Name: id}, Status: status}
}

// Only the heads that answered tick. A head that failed did not produce the
// work being handed on, so recording an event for it would claim something
// that did not happen.
func TestWriteFanoutHandoff_TicksOnlyTheHeadsThatAnswered(t *testing.T) {
	testutil.NewSandbox(t)

	writeFanoutHandoff("hydra-ensemble", "SPRT ensemble", "is this safe?", "yes",
		"internal/auth/token.go", []swarm.Attempt{
			fanoutAttempt("ollama/b", swarm.StatusOK),
			fanoutAttempt("ollama/dead", swarm.StatusFailed),
			fanoutAttempt("ollama/a", swarm.StatusOK),
			fanoutAttempt("ollama/skipped", swarm.StatusCanceled),
		})

	h, err := a2a.Load(filepath.Join(config.Dir(), "logs", "last_handoff.json"))
	if err != nil {
		t.Fatalf("a fan-out wrote no handoff: %v", err)
	}
	if len(h.Clock) != 2 {
		t.Errorf("clock = %v, want only the two heads that answered", h.Clock)
	}
	for _, want := range []string{"ollama/a", "ollama/b"} {
		if h.Clock[want] != 1 {
			t.Errorf("clock[%q] = %d, want 1", want, h.Clock[want])
		}
	}
	// --file is the flag most likely to name a contended file, and the one
	// whose handoff was never written, so the file has to survive to here or
	// ConflictsWith has nothing to overlap.
	if len(h.Files) != 1 || h.Files[0] != "internal/auth/token.go" {
		t.Errorf("Files = %v, want the file the run was about", h.Files)
	}
	if h.From != "hydra-ensemble" || h.PriorOutput != "yes" {
		t.Errorf("handoff = %+v, want the ensemble's identity and answer", h)
	}
}

// A fan-out where nothing answered is not an event: the prior handoff stays
// the newest rather than being replaced by one recording no work.
func TestWriteFanoutHandoff_NothingAnsweredWritesNothing(t *testing.T) {
	testutil.NewSandbox(t)
	path := filepath.Join(config.Dir(), "logs", "last_handoff.json")

	writeFanoutHandoff("hydra-swarm", "swarm · race", "p", "", "", []swarm.Attempt{
		fanoutAttempt("ollama/a", swarm.StatusFailed),
	})

	// a2a.Load reports a missing handoff as (nil, nil), so the nil is the
	// assertion and an err check alone passes either way.
	h, err := a2a.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if h != nil {
		t.Errorf("a fan-out where every head failed wrote %+v, claiming work that "+
			"did not happen", h)
	}
}

// Two runs over the same heads must produce the same clock whatever order the
// fan-out finished in. It holds because Tick increments a map rather than
// appending to a list, so no sort is needed; asserted rather than assumed,
// because I added a sort.Strings here first and only a mutation test showed it
// changed nothing.
func TestWriteFanoutHandoff_OrderOfCompletionDoesNotChangeTheClock(t *testing.T) {
	testutil.NewSandbox(t)
	path := filepath.Join(config.Dir(), "logs", "last_handoff.json")

	writeFanoutHandoff("hydra-swarm", "swarm · all", "p", "ok", "", []swarm.Attempt{
		fanoutAttempt("ollama/c", swarm.StatusOK),
		fanoutAttempt("ollama/a", swarm.StatusOK),
	})
	first, err := a2a.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	testutil.NewSandbox(t) // a fresh home, so the second run starts from nothing too
	writeFanoutHandoff("hydra-swarm", "swarm · all", "p", "ok", "", []swarm.Attempt{
		fanoutAttempt("ollama/a", swarm.StatusOK),
		fanoutAttempt("ollama/c", swarm.StatusOK),
	})
	second, err := a2a.Load(filepath.Join(config.Dir(), "logs", "last_handoff.json"))
	if err != nil {
		t.Fatal(err)
	}

	if got := first.Clock.Compare(second.Clock); got != a2a.Equal {
		t.Errorf("two runs over the same heads compare %v, want equal: the clock "+
			"depends on completion order", got)
	}
}
