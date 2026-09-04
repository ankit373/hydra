// SPDX-License-Identifier: MIT

package swarm

import (
	"context"
	"fmt"
	"math"

	"github.com/ankit373/hydra/internal/trust"
)

// CalibratedJudge is a Dawid-Skene naive-Bayes combiner over the K distinct
// answers in a swarm round — the batch dual of trust.Run's sequential SPRT,
// scoring every candidate hypothesis at once instead of picking via a single
// LLM judge call or a static CapScore rank. Errors when every hypothesis'
// Λ is 0 (no calibration data anywhere), so CompositeJudge falls through to
// LLMJudge/CapScoreJudge unchanged.
type CalibratedJudge struct {
	cal    *trust.Calibrator
	domain string
	equiv  trust.AnswerEquivalence // nil → trust.TextEquivalence (no extra LLM calls)
}

// newCalibratedJudge defaults a nil equiv to trust.TextEquivalence, keeping
// ModeBest's judge overhead at zero extra LLM calls unless a caller opts in.
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
	recordSwarmCoAgreement(j.domain, successful, attempts, j.equiv)

	// Each successful attempt's LLR only ever takes one of two values — agree
	// or disagree with whichever hypothesis is being scored — so both are
	// precomputed once per attempt here instead of lambdaFor calling the
	// calibrator (an RWMutex-guarded map lookup) once per (hypothesis, attempt)
	// pair: O(N) lookups total instead of O(N×K) for K hypotheses.
	llrTrue := make(map[int]float64, len(successful))
	llrFalse := make(map[int]float64, len(successful))
	for _, idx := range successful {
		llrTrue[idx] = j.cal.LLR(attempts[idx].Head.ID, j.domain, true)
		llrFalse[idx] = j.cal.LLR(attempts[idx].Head.ID, j.domain, false)
	}

	lambda := make([]float64, len(groups))
	for k := range groups {
		lambda[k] = j.lambdaFor(groups[k], successful, attempts, llrTrue, llrFalse)
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

	winner := representative(groups[best], attempts, llrTrue)
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

// lambdaFor computes Λ_k: every attempt votes agree/disagree with hypothesis,
// weighted by its calibrated LLR (precomputed once per attempt by Judge, not
// recomputed here), same-family repeats discounted.
func (j *CalibratedJudge) lambdaFor(hypothesis agreementGroup, successful []int, attempts []Attempt, llrTrue, llrFalse map[int]float64) float64 {
	inGroup := make(map[int]bool, len(hypothesis.members))
	for _, idx := range hypothesis.members {
		inGroup[idx] = true
	}
	seenFamily := map[string]bool{}
	var lambda float64
	for _, idx := range successful {
		llr := llrFalse[idx]
		if inGroup[idx] {
			llr = llrTrue[idx]
		}
		if fam := attempts[idx].Head.Provider; fam != "" {
			if seenFamily[fam] {
				llr *= trust.FamilyDiscount(trust.DefaultCoAgreementPath(), fam)
			}
			seenFamily[fam] = true
		}
		lambda += llr
	}
	return lambda
}

// representative breaks the tie among a hypothesis's equally-valid members by
// individual evidence, then CapScore. llrTrue is Judge's precomputed
// LLR(id, domain, true) per attempt — always the "true" branch here since
// representative asks who best supports having actually said the right thing.
func representative(g agreementGroup, attempts []Attempt, llrTrue map[int]float64) int {
	best := g.members[0]
	bestLLR := llrTrue[best]
	for _, idx := range g.members[1:] {
		llr := llrTrue[idx]
		if llr > bestLLR || (llr == bestLLR && attempts[idx].Head.CapScore > attempts[best].Head.CapScore) {
			best, bestLLR = idx, llr
		}
	}
	return best
}

// clusterByAgreement greedily groups attempts whose outputs equiv treats as the same answer.
func clusterByAgreement(attempts []Attempt, successful []int, equiv trust.AnswerEquivalence) []agreementGroup {
	texts := make([]string, len(successful))
	for i, idx := range successful {
		texts[i] = attempts[idx].Output
	}
	groups := make([]agreementGroup, 0, len(texts))
	for _, g := range trust.ClusterByAgreement(texts, equiv) {
		members := make([]int, len(g))
		for i, localIdx := range g {
			members[i] = successful[localIdx]
		}
		groups = append(groups, agreementGroup{members: members})
	}
	return groups
}

// recordSwarmCoAgreement feeds ModeBest's own agreement structure into the
// same coupling measurement trust.Run's SPRT path feeds, so a family's
// FamilyDiscount improves from every ensembling path, not just --confidence.
func recordSwarmCoAgreement(domain string, successful []int, attempts []Attempt, equiv trust.AnswerEquivalence) {
	ids := make([]string, len(successful))
	families := make([]string, len(successful))
	texts := make([]string, len(successful))
	for i, idx := range successful {
		ids[i] = attempts[idx].Head.ID
		families[i] = attempts[idx].Head.Provider
		texts[i] = attempts[idx].Output
	}
	trust.RecordCoAgreement(trust.DefaultCoAgreementPath(), domain, ids, families, texts, equiv)
}

func allZero(xs []float64) bool {
	for _, x := range xs {
		if x != 0 {
			return false
		}
	}
	return true
}

// softmax is the K-ary generalization of the sigmoid trust.Run uses for its binary decision.
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
