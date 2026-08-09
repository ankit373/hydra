// SPDX-License-Identifier: MIT

package swarm

import (
	"context"
	"fmt"
	"math"

	"github.com/ankit373/hydra/internal/trust"
)

// CalibratedJudge is a Dawid-Skene naive-Bayes combiner (Dawid & Skene, 1979:
// P(votes|z=k) = Π_i C_i[k,vote_i] over each rater's confusion matrix) over
// the K distinct answers in a completed swarm round, using trust.Calibrator's
// already-estimated per-source confusion matrices as the emission model.
//
// It is the batch/simultaneous dual of trust.Run's SPRT ensemble: SPRT
// accumulates the identical LLR evidence sequentially against one evolving
// candidate with early stopping, which fits sampling sources one at a time at
// a cost; ModeBest already has all N attempts in hand with nothing left to
// sample, so the right move is to score every candidate hypothesis at once
// (see lambdaFor) and take the log-posterior argmax, rather than pick a
// winner via a single independent LLM judge call or a static CapScore rank —
// neither of which is calibration-aware, and both of which are exactly the
// naive-voting shape the correlated-error literature (e.g. Zhou et al.,
// "Variation in Verification," arXiv:2509.17995) finds captures only a
// fraction of the available gain.
//
// trust.CorrelationDiscount — reused, not duplicated — is the mean-field
// correction for the one documented way the naive-Bayes independence
// assumption breaks: models make more correlated errors as they get
// stronger, so a repeat vote from an already-seen model family is discounted
// rather than counted as independent confirmation.
//
// Returns an error when every hypothesis' Λ is exactly 0 — every source is
// uncalibrated (se=sp=0.5 ⟹ LLR≡0) — letting CompositeJudge fall through to
// LLMJudge/CapScoreJudge unchanged. Any install that has never run `hyctl
// trust record` gets byte-identical behavior to before this existed.
type CalibratedJudge struct {
	cal    *trust.Calibrator
	domain string
	equiv  trust.AnswerEquivalence // nil → trust.TextEquivalence (no extra LLM calls)
}

// newCalibratedJudge constructs a CalibratedJudge. A nil equiv defaults to
// trust.TextEquivalence rather than a semantic (LLM-backed) comparator: this
// mirrors trust.Run's own v1-default-is-textual, semantic-is-opt-in pattern,
// and keeps ModeBest's cheapest-mode judge overhead at zero extra LLM calls —
// spending more model calls to fix naive voting would be a regression in
// exactly the resource this rework is meant to protect.
func newCalibratedJudge(cal *trust.Calibrator, domain string, equiv trust.AnswerEquivalence) *CalibratedJudge {
	if equiv == nil {
		equiv = trust.TextEquivalence
	}
	return &CalibratedJudge{cal: cal, domain: domain, equiv: equiv}
}

// agreementGroup is one candidate hypothesis: the cluster of attempt indices
// whose outputs the judge's equivalence function treats as the same answer.
type agreementGroup struct {
	members []int // indices into the attempts slice
}

func (j *CalibratedJudge) Judge(_ context.Context, _ string, attempts []Attempt) (*JudgeVerdict, error) {
	successful := successfulAttempts(attempts)
	if len(successful) == 0 {
		return nil, fmt.Errorf("calibrated judge: no successful attempts to evaluate")
	}
	if len(successful) == 1 {
		idx := successful[0]
		scores := make([]int, len(attempts))
		scores[idx] = attempts[idx].Head.CapScore
		return &JudgeVerdict{
			WinnerIndex: idx,
			Scores:      scores,
			Reason:      "only one successful response",
		}, nil
	}

	groups := clusterByAgreement(attempts, successful, j.equiv)
	lambda := make([]float64, len(groups))
	for k := range groups {
		lambda[k] = j.lambdaFor(groups[k], successful, attempts)
	}

	best := 0
	for k := range lambda {
		if lambda[k] > lambda[best] {
			best = k
		}
	}
	if allZero(lambda) {
		return nil, fmt.Errorf("calibrated judge: no calibration data for any candidate in domain %q", j.domain)
	}

	winner := representative(groups[best], attempts, j.cal, j.domain)
	probs := softmax(lambda)

	scores := make([]int, len(attempts))
	for k, g := range groups {
		share := int(math.Round(100 * probs[k]))
		for _, idx := range g.members {
			scores[idx] = share
		}
	}

	return &JudgeVerdict{
		WinnerIndex: winner,
		Scores:      scores,
		Reason: fmt.Sprintf(
			"Dawid-Skene MAP over %d candidate answers: %s's cluster carries %.0f%% posterior weight",
			len(groups), attempts[winner].Head.Name, 100*probs[best],
		),
	}, nil
}

// lambdaFor computes Λ_k, the Dawid-Skene log-posterior (uniform prior) of
// the hypothesis "hypothesis is the correct answer": every successful
// attempt casts an implicit binary vote — agree if it's a member of
// hypothesis, disagree otherwise — weighted by that source's calibrated LLR
// for the vote it cast, with a same-family repeat discounted per
// trust.CorrelationDiscount (family-seen state is local to this one
// hypothesis's tally, since a different hypothesis reassigns which attempts
// agree vs. disagree).
func (j *CalibratedJudge) lambdaFor(hypothesis agreementGroup, successful []int, attempts []Attempt) float64 {
	inGroup := make(map[int]bool, len(hypothesis.members))
	for _, idx := range hypothesis.members {
		inGroup[idx] = true
	}
	seenFamily := map[string]bool{}
	var lambda float64
	for _, idx := range successful {
		llr := j.cal.LLR(attempts[idx].Head.ID, j.domain, inGroup[idx])
		if fam := attempts[idx].Head.Provider; fam != "" {
			if seenFamily[fam] {
				llr *= trust.CorrelationDiscount
			}
			seenFamily[fam] = true
		}
		lambda += llr
	}
	return lambda
}

// representative picks which member of the winning hypothesis becomes the
// returned WinnerIndex. Every member agrees (they're the same answer by
// construction), so this is a tie-break, not a second decision: the member
// with the strongest individual evidence for correctness, then CapScore.
func representative(g agreementGroup, attempts []Attempt, cal *trust.Calibrator, domain string) int {
	best := g.members[0]
	bestLLR := cal.LLR(attempts[best].Head.ID, domain, true)
	for _, idx := range g.members[1:] {
		llr := cal.LLR(attempts[idx].Head.ID, domain, true)
		if llr > bestLLR || (llr == bestLLR && attempts[idx].Head.CapScore > attempts[best].Head.CapScore) {
			best, bestLLR = idx, llr
		}
	}
	return best
}

// clusterByAgreement groups successful attempt indices whose outputs the
// equivalence function treats as the same answer. Greedy: each attempt joins
// the first existing group it agrees with (compared against that group's
// first member), or starts a new group. Attempt counts here are bounded by
// MaxHeads (default 5), so this is cheap regardless of the O(n²) shape.
func clusterByAgreement(attempts []Attempt, successful []int, equiv trust.AnswerEquivalence) []agreementGroup {
	var groups []agreementGroup
	for _, idx := range successful {
		placed := false
		for gi := range groups {
			rep := groups[gi].members[0]
			if equiv(attempts[rep].Output, attempts[idx].Output) {
				groups[gi].members = append(groups[gi].members, idx)
				placed = true
				break
			}
		}
		if !placed {
			groups = append(groups, agreementGroup{members: []int{idx}})
		}
	}
	return groups
}

func allZero(xs []float64) bool {
	for _, x := range xs {
		if x != 0 {
			return false
		}
	}
	return true
}

// softmax turns K hypotheses' log-posteriors into a normalized probability
// distribution — the direct K-ary generalization of sigmoid, which is what
// trust.Run already uses to turn a single Λ into P(correct) for its binary
// (accept candidate / reject candidate) decision.
func softmax(lambda []float64) []float64 {
	max := lambda[0]
	for _, l := range lambda[1:] {
		if l > max {
			max = l
		}
	}
	sum := 0.0
	exp := make([]float64, len(lambda))
	for i, l := range lambda {
		exp[i] = math.Exp(l - max)
		sum += exp[i]
	}
	probs := make([]float64, len(lambda))
	for i := range exp {
		probs[i] = exp[i] / sum
	}
	return probs
}
