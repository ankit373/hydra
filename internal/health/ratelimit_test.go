// SPDX-License-Identifier: MIT

package health

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// busy stands in for executor.StatusError, which this package deliberately
// does not import. The interface is what couples them, so the test asserts
// against the interface too.
type busy struct {
	msg   string
	after time.Duration
	is    bool
}

func (b busy) Error() string { return b.msg }
func (b busy) RateLimitRetryAfter() (time.Duration, bool) {
	return b.after, b.is
}

// A rate limit is a third thing: it recurs until a stated time and then stops.
// Calling it Transient is what made the head sit out on our schedule instead
// of the server's.
func TestClassify_ARateLimitIsItsOwnKind(t *testing.T) {
	if got := Classify(busy{msg: "status 429", after: 2 * time.Second, is: true}); got != RateLimited {
		t.Errorf("Classify(429) = %v, want RateLimited", got)
	}
}

// The structural check runs before the substring pass, because a 429 body is
// provider text and can contain a fatal signal word by accident. Parking a
// busy head permanently because its error page said "model not found" would
// be the worst of both.
func TestClassify_A429BodyNamingAFatalSignalIsStillARateLimit(t *testing.T) {
	e := busy{msg: `status 429, {"error":"no such model in this tier, slow down"}`,
		after: time.Second, is: true}
	if got := Classify(e); got != RateLimited {
		t.Errorf("Classify = %v, want RateLimited; the substring pass ran first", got)
	}
}

// Wrapped errors are the normal shape by the time dispatch sees one.
func TestStatedWait_ReadsThroughAWrap(t *testing.T) {
	wrapped := fmt.Errorf("dispatch: %w", busy{msg: "429", after: 3 * time.Second, is: true})
	d, ok := StatedWait(wrapped)
	if !ok || d != 3*time.Second {
		t.Errorf("StatedWait through a wrap = (%v, %v), want (3s, true)", d, ok)
	}
}

func TestStatedWait_OrdinaryErrorsCarryNone(t *testing.T) {
	if _, ok := StatedWait(errors.New("connection refused")); ok {
		t.Error("an ordinary error must not report a stated wait")
	}
}

// The server asked, so waiting its two seconds beats waiting our minute. This
// is the measured defect: a head that said 2s sat out for a minute, and every
// dispatch in between silently ran on a weaker head.
func TestFailFor_AStatedWaitReplacesTheBackoff(t *testing.T) {
	s := &Store{heads: map[string]*entry{}}
	now := time.Now()

	s.FailFor("h", "429", RateLimited, now, 2*time.Second)

	e := s.heads["h"]
	if got := e.RetryAt.Sub(now); got != 2*time.Second {
		t.Errorf("parked for %v, want the stated 2s", got)
	}
}

// Parks on the first one. softFailuresBeforeOpen exists because a first
// failure might not recur; a 429 already told us it will, until a stated time.
func TestFailFor_AStatedWaitParksOnTheFirstFailure(t *testing.T) {
	s := &Store{heads: map[string]*entry{}}
	now := time.Now()

	s.FailFor("h", "429", RateLimited, now, time.Second)

	if _, open := s.Blocked("h", now.Add(500*time.Millisecond)); !open {
		t.Error("a head the server asked us to leave alone was retried inside its own window")
	}
}

// Each 429 restates the answer, so compounding our doubling on top of it is
// how two seconds became thirty minutes.
func TestFailFor_RepeatedRateLimitsDoNotCompound(t *testing.T) {
	s := &Store{heads: map[string]*entry{}}
	now := time.Now()

	for i := 0; i < 6; i++ {
		s.FailFor("h", "429", RateLimited, now, 2*time.Second)
	}

	if got := s.heads["h"].RetryAt.Sub(now); got != 2*time.Second {
		t.Errorf("after six rate limits the wait is %v, want the stated 2s every time", got)
	}
}

// A server may ask for a day. Our own ceiling still applies, or one header
// removes a head for longer than any failure would.
func TestFailFor_AnAbsurdStatedWaitIsClamped(t *testing.T) {
	s := &Store{heads: map[string]*entry{}}
	now := time.Now()

	s.FailFor("h", "429", RateLimited, now, 24*time.Hour)

	if got := s.heads["h"].RetryAt.Sub(now); got != maxCooldown {
		t.Errorf("parked for %v, want the %v ceiling", got, maxCooldown)
	}
}

// A 429 naming no time falls back to the existing schedule, because without a
// number we know no better than before.
func TestFailFor_NoStatedWaitKeepsTheOrdinaryBackoff(t *testing.T) {
	s := &Store{heads: map[string]*entry{}}
	now := time.Now()

	s.FailFor("h", "429", RateLimited, now, 0)
	s.FailFor("h", "429", RateLimited, now, 0)

	if got := s.heads["h"].RetryAt.Sub(now); got != baseCooldown {
		t.Errorf("with no stated wait the head parked for %v, want the %v backoff", got, baseCooldown)
	}
}

// Fail is FailFor with nothing stated, so every existing caller is unchanged.
func TestFail_IsUnchangedForEveryOtherKind(t *testing.T) {
	a := &Store{heads: map[string]*entry{}}
	b := &Store{heads: map[string]*entry{}}
	now := time.Now()

	a.Fail("h", "boom", Transient, now)
	a.Fail("h", "boom", Transient, now)
	b.FailFor("h", "boom", Transient, now, 0)
	b.FailFor("h", "boom", Transient, now, 0)

	if a.heads["h"].RetryAt != b.heads["h"].RetryAt {
		t.Error("Fail and FailFor with no stated wait disagree")
	}
}
