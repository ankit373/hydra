// SPDX-License-Identifier: MIT

package idle

import (
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/pricing"
)

// opus is the worked example: input 1, write 1.25x, read 0.05x.
var opus = Rates{Write: 1.25, Read: 0.05, Known: true}

func TestShouldSwap_ColdAlwaysSwaps(t *testing.T) {
	if ok, why := ShouldSwap(180000, 20000, 0, opus, false); !ok || !strings.Contains(why, "cold") {
		t.Fatalf("cold cache: %v %q, want a swap", ok, why)
	}
	if ok, _ := ShouldSwap(180000, 20000, 0, Rates{}, false); !ok {
		t.Fatal("cold cache with unknown rates must still swap")
	}
	if ok, _ := ShouldSwap(20000, 20000, 9, opus, false); ok {
		t.Fatal("a pack that is not smaller was swapped in")
	}
}

// N 180K, M 20K, w 1.25, r 0.05: the rewrite pays back after 3.125 requests.
func TestShouldSwap_WarmBreakEven(t *testing.T) {
	for _, c := range []struct {
		turns float64
		want  bool
	}{{2, false}, {3, false}, {3.125, false}, {3.2, true}, {4, true}, {10, true}} {
		if ok, why := ShouldSwap(180000, 20000, c.turns, opus, true); ok != c.want || !strings.Contains(why, "3.1") {
			t.Errorf("turns %v: %v %q, want %v naming the 3.1 break-even", c.turns, ok, why, c.want)
		}
	}
}

func TestShouldSwap_UnknownRatesWaitWhileWarm(t *testing.T) {
	if ok, why := ShouldSwap(180000, 20000, 100, Rates{}, true); ok || !strings.Contains(why, "not known") {
		t.Fatalf("%v %q, want no swap while warm with unknown rates", ok, why)
	}
	if ok, _ := ShouldSwap(180000, 20000, 100, Rates{Write: 1.25, Known: true}, true); ok {
		t.Fatal("a free read can never repay a rewrite")
	}
}

func TestRatesFor(t *testing.T) {
	w, r := 6.25, 0.5
	got := RatesFor(pricing.ModelPrice{InputPerMillion: 5, CacheWritePerMillion: &w, CacheReadPerMillion: &r})
	if !got.Known || got.Write != 1.25 || got.Read != 0.1 {
		t.Fatalf("rates %+v, want write 1.25 and read 0.1", got)
	}
	if RatesFor(pricing.ModelPrice{InputPerMillion: 5, CacheReadPerMillion: &r}).Known {
		t.Fatal("a missing write rate read as known")
	}
}

func TestWarm(t *testing.T) {
	if !Warm(4*time.Minute) || Warm(5*time.Minute) || Warm(time.Hour) {
		t.Fatal("warm is under five minutes")
	}
}

func TestTurnsLeft_MedianBurstAfterAPause(t *testing.T) {
	base := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	at := func(mins ...int) []time.Time {
		var out []time.Time
		for _, m := range mins {
			out = append(out, base.Add(time.Duration(m)*time.Minute))
		}
		return out
	}
	gap := 10 * time.Minute
	// Bursts after a pause of 10+ minutes: 3 requests (30, 31, 32), then 5 (60..64).
	times := at(0, 1, 2, 30, 31, 32, 60, 61, 62, 63, 64)
	if got := TurnsLeft(times, gap, 7); got != 4 {
		t.Fatalf("median of bursts 3 and 5 = %v, want 4", got)
	}
	if got := TurnsLeft(at(0, 1, 2, 3), gap, 7); got != 7 {
		t.Fatalf("no pause in the history = %v, want the fallback 7", got)
	}
	if got := TurnsLeft(nil, gap, 7); got != 7 {
		t.Fatalf("no history = %v, want the fallback 7", got)
	}
}

func TestTurnsLeft_OddNumberOfBursts(t *testing.T) {
	base := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	at := func(mins ...int) []time.Time {
		var out []time.Time
		for _, m := range mins {
			out = append(out, base.Add(time.Duration(m)*time.Minute))
		}
		return out
	}
	// After each 10-minute pause: 2, 3 and 6 requests; the median is 3.
	times := at(0, 30, 31, 60, 61, 62, 100, 101, 102, 103, 104, 105)
	if got := TurnsLeft(times, 10*time.Minute, 7); got != 3 {
		t.Fatalf("median of 2, 3, 6 = %v, want 3", got)
	}
}
