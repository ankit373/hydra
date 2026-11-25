// SPDX-License-Identifier: MIT

package aver

import (
	"fmt"
	"math"
	"sort"

	"github.com/ankit373/hydra/internal/stats"
)

// AgreeFeature names the feature "at least m earlier answers gave the same answer".
func AgreeFeature(m int) string { return fmt.Sprintf("agree>=%d", m) }

// Pattern is a set of features that must all pass. RBar bounds how often a
// wrong answer matches it, under any dependence between the features.
type Pattern struct {
	Features []string `json:"features"`
	Wrong    int      `json:"wrong"`
	Matched  int      `json:"matched"`
	RBar     float64  `json:"r_bar"`
	S        float64  `json:"s"`
}

// NotVerified is why no pattern can certify: Need is the wrong answers it would take.
type NotVerified struct {
	Reason string `json:"reason"`
	Have   int    `json:"have"`
	Need   int    `json:"need"`
}

// ReasonNoPattern is why pattern mode refuses with enough data; ReasonTooFewWrong is the other.
const ReasonNoPattern = "no pattern separates right from wrong"

// Family is every candidate pattern: 1 to 3 features from checks and agreements,
// at most one of them an agreement. Sorted, so the family never depends on map order.
func Family(checks, agreements []string) [][]string {
	cs := append([]string(nil), checks...)
	sort.Strings(cs)
	var out [][]string
	var walk func(start int, cur []string)
	walk = func(start int, cur []string) {
		if len(cur) > 0 {
			out = append(out, append([]string(nil), cur...))
		}
		if len(cur) == 3 {
			return
		}
		for i := start; i < len(cs); i++ {
			walk(i+1, append(cur, cs[i]))
		}
	}
	walk(0, nil)
	singles := len(out)
	for _, a := range agreements {
		out = append(out, []string{a})
		for _, p := range out[:singles] {
			if len(p) < 3 {
				out = append(out, append(append([]string(nil), p...), a))
			}
		}
	}
	return out
}

func matches(a Answer, features []string) bool {
	for _, f := range features {
		if !a.Verdicts[f] {
			return false
		}
	}
	return true
}

// NeedWrong is the wrong answers a pattern needs to reach bar with no wrong
// match: 1/r̄ >= bar at k = 0 needs n >= ln(family/delta) / -ln(1 - 1/bar) ~ ln(family/delta) bar.
func NeedWrong(family int, delta, bar float64) int {
	return int(math.Ceil(math.Log(float64(family)/delta) * bar))
}

// FitPattern bounds each pattern's match rate on the wrong answers at delta/len(family),
// and picks, on these answers only, the one that clears bar and passes most right answers.
func FitPattern(answers []Answer, family [][]string, bar, delta float64) (Pattern, *NotVerified) {
	var wrong, right []Answer
	for _, a := range answers {
		if a.Correct {
			right = append(right, a)
		} else {
			wrong = append(wrong, a)
		}
	}
	need := NeedWrong(len(family), delta, bar)
	if len(wrong) == 0 || len(family) == 0 {
		return Pattern{}, &NotVerified{Reason: ReasonTooFewWrong, Have: len(wrong), Need: need}
	}
	level := delta / float64(len(family))
	var best *Pattern
	for _, f := range family {
		p := Pattern{Features: f, Wrong: len(wrong)}
		for _, a := range wrong {
			if matches(a, f) {
				p.Matched++
			}
		}
		// Cannot fail: Wrong > 0, Matched <= Wrong, level in (0, 1).
		p.RBar, _ = stats.ClopperPearsonUpper(p.Matched, p.Wrong, level)
		if 1/p.RBar < bar {
			continue
		}
		hit := 0
		for _, a := range right {
			if matches(a, f) {
				hit++
			}
		}
		if len(right) > 0 {
			p.S = float64(hit) / float64(len(right))
		}
		if best == nil || p.S > best.S || (p.S == best.S && len(p.Features) < len(best.Features)) {
			best = &p
		}
	}
	if best == nil {
		reason := ReasonNoPattern
		if len(wrong) < need {
			reason = ReasonTooFewWrong
		}
		return Pattern{}, &NotVerified{Reason: reason, Have: len(wrong), Need: need}
	}
	return *best, nil
}
