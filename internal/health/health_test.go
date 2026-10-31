// SPDX-License-Identifier: MIT

package health

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/provider"
)

func store(t *testing.T) *Store {
	t.Helper()
	return Open(filepath.Join(t.TempDir(), "health.json"))
}

// A missing binary is not going to appear in sixty seconds, and the run that
// prompted all this tried eight heads that had each already proven it.
func TestFatalFailureParksTheHeadOnFirstSight(t *testing.T) {
	s, now := store(t), time.Now()
	s.Fail("opus", "agy not found", Fatal, now)

	why, parked := s.Blocked("opus", now)
	if !parked {
		t.Fatal("a fatal failure left the head in rotation")
	}
	if want := "agy not found"; !strings.Contains(why, want) {
		t.Errorf("reason = %q, want it to carry %q", why, want)
	}
	if !strings.Contains(why, "retrying") {
		t.Errorf("reason = %q, want it to say when it will be retried", why)
	}
}

// A timeout or a 500 might not recur, and parking a head on one is how a
// working fleet talks itself into having no heads left.
func TestTransientFailureGetsASecondChance(t *testing.T) {
	s, now := store(t), time.Now()
	s.Fail("flash", "timeout", Transient, now)
	if _, parked := s.Blocked("flash", now); parked {
		t.Fatal("one transient failure parked the head")
	}
	s.Fail("flash", "timeout", Transient, now)
	if _, parked := s.Blocked("flash", now); !parked {
		t.Error("two transient failures did not park the head")
	}
}

func TestAParkedHeadComesBackOnItsOwn(t *testing.T) {
	s, now := store(t), time.Now()
	s.Fail("opus", "agy not found", Fatal, now)

	if _, parked := s.Blocked("opus", now.Add(baseCooldown-time.Second)); !parked {
		t.Error("head was let back in before its cooldown elapsed")
	}
	if _, parked := s.Blocked("opus", now.Add(baseCooldown+time.Second)); parked {
		t.Error("head never came back; a breaker with no exit is a permanent outage")
	}
}

// The breaker is closed by something working, not by time passing: a head let
// through on trial that fails again must back off further, not be retried on
// every dispatch.
func TestOnlySuccessClosesTheBreaker(t *testing.T) {
	s, now := store(t), time.Now()
	s.Fail("opus", "boom", Fatal, now)
	after := now.Add(baseCooldown + time.Second)

	s.Fail("opus", "boom", Fatal, after)
	if _, parked := s.Blocked("opus", after); !parked {
		t.Fatal("a failed trial did not re-park the head")
	}

	s.Pass("opus")
	if _, parked := s.Blocked("opus", after); parked {
		t.Error("a success did not clear the head")
	}
}

func TestCooldownBacksOffAndIsCapped(t *testing.T) {
	first, second := cooldown(softFailuresBeforeOpen), cooldown(softFailuresBeforeOpen+1)
	if second <= first {
		t.Errorf("cooldown did not grow: %v then %v", first, second)
	}
	if got := cooldown(100); got != maxCooldown {
		t.Errorf("cooldown(100) = %v, want it capped at %v", got, maxCooldown)
	}
}

func TestStateSurvivesTheProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "health.json")
	now := time.Now()

	s := Open(path)
	s.Fail("opus", "agy not found", Fatal, now)
	if err := s.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if _, parked := Open(path).Blocked("opus", now); !parked {
		t.Error("a parked head was forgotten when the process ended")
	}
}

// Breaker state is a cache, not work someone is waiting on. Refusing to
// dispatch because this file is corrupt would turn a bad file into an outage.
func TestACorruptStoreStartsEmptyRatherThanFailing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "health.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, parked := Open(path).Blocked("opus", time.Now()); parked {
		t.Error("a corrupt store parked a head it knows nothing about")
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want Kind
	}{
		{"missing binary", exec.ErrNotFound, Fatal},
		{"wrapped missing binary", errors.New(`exec: "agy": executable file not found in $PATH`), Fatal},
		{"model agy does not have", errors.New(`invalid model selection (--model "gemini-3.5-flash")`), Fatal},
		{"timeout", errors.New("context deadline exceeded"), Transient},
		{"overloaded", errors.New("model overloaded, try again"), Transient},
		{"none", nil, Transient},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.err); got != c.want {
				t.Errorf("Classify(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// "agy is not installed" is a better answer than "failed 3 minutes ago", and
// it is the cause of the other.
func TestReasonPrefersThePreconditionOverTheBreaker(t *testing.T) {
	s, now := store(t), time.Now()
	broken := provider.Head{ID: "opus", Source: "registry"} // no resolved binary
	s.Fail("opus", "failed earlier", Fatal, now)

	if why := Reason(s, broken, now); !strings.Contains(why, "agy CLI is not on PATH") {
		t.Errorf("Reason = %q, want the precondition", why)
	}
}

func TestReasonFallsThroughToTheBreaker(t *testing.T) {
	s, now := store(t), time.Now()
	ok := provider.Head{ID: "opus", Source: "registry", Executable: "/usr/bin/agy"}

	if why := Reason(s, ok, now); why != "" {
		t.Fatalf("a healthy head was reported unroutable: %q", why)
	}
	s.Fail("opus", "overloaded", Fatal, now)
	if why := Reason(s, ok, now); !strings.Contains(why, "overloaded") {
		t.Errorf("Reason = %q, want the breaker's reason", why)
	}
}

// A nil store is what every caller gets before New has run. It must read as
// "nothing known", never panic.
func TestANilStoreIsInert(t *testing.T) {
	var s *Store
	if _, parked := s.Blocked("opus", time.Now()); parked {
		t.Error("a nil store parked a head")
	}
	s.Fail("opus", "boom", Fatal, time.Now())
	s.Pass("opus")
	if err := s.Flush(); err != nil {
		t.Errorf("Flush on a nil store = %v", err)
	}
}
