// SPDX-License-Identifier: MIT

package trust

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Target configures an SPRT run. The domain used for calibration lookups comes
// from the Task, not here, so there is exactly one source of truth for it.
type Target struct {
	Confidence float64 // desired P(correct), e.g. 0.95 → α = 1-conf
	MaxCostUSD float64 // hard spend ceiling; 0 = no limit
}

// Source is the minimal view of a head that SPRT needs. Kept independent of
// provider.Head so the trust package stays a low-level dependency; callers
// (swarm/dispatch) adapt their heads to this in Phase 2b.
type Source struct {
	ID         string // calibration key, e.g. "model:claude-sonnet"
	Family     string // base-model family, for the correlation discount ("" = independent)
	EstCostUSD float64
}

// Answer is what a source returned for the task.
type Answer struct {
	Text    string
	CostUSD float64 // actual cost if known; falls back to Source.EstCostUSD
}

// Executor runs one source against a task. Production wraps the swarm executor;
// tests inject a deterministic fake.
type Executor interface {
	Execute(ctx context.Context, src Source, task Task) (Answer, error)
}

// Decision is how an SPRT run ended.
type Decision int

const (
	// DecisionAccept: some answer's Λ crossed the accept threshold A at the
	// target confidence.
	DecisionAccept Decision = iota
	// DecisionStoppedOnBudget: ran out of budget/heads before reaching A, the
	// residual uncertainty is where a human oracle (review) should be spent.
	DecisionStoppedOnBudget
)

func (d Decision) String() string {
	if d == DecisionAccept {
		return "accept"
	}
	return "stopped_on_budget"
}

// Evidence is one entry in the LLR ledger, a single source's calibrated
// contribution to the running log-odds that the candidate is correct.
type Evidence struct {
	Source      string  `json:"source"`
	Agreed      bool    `json:"agreed"` // matched the candidate at the time
	LLR         float64 `json:"llr"`    // calibrated contribution (nats), after any discount
	Candidate   string  `json:"candidate"`
	LambdaAfter float64 `json:"lambda_after"`
	CostUSD     float64 `json:"cost_usd"`

	// ConfidenceAfter is σ(LambdaAfter), the running P(correct) once this
	// source had been weighed. Stored rather than left for readers to derive:
	// the ledger is a public JSON type written to trust.jsonl and read by the
	// run log, and a second copy of the sigmoid in each reader is a third place
	// for the confidence to drift.
	ConfidenceAfter float64 `json:"confidence_after"`
}

// Hypothesis is one candidate answer under test, with the evidence for it.
// Every distinct answer is tracked for the whole run, so nothing has to be
// discarded when the leader changes.
type Hypothesis struct {
	Answer     string  `json:"answer"`
	Lambda     float64 `json:"lambda"`     // log-odds this answer is the correct one
	Confidence float64 `json:"confidence"` // σ(Lambda)
	Votes      int     `json:"votes"`      // sources whose answer matched it
}

// Result is the outcome of Run. Confidence and Lambda describe the leading
// hypothesis; Hypotheses carries every one that was under test.
type Result struct {
	Candidate  string       `json:"candidate"`
	Confidence float64      `json:"confidence"` // σ(Λ) of the leader
	Decision   Decision     `json:"decision"`
	Lambda     float64      `json:"lambda"`
	SpentUSD   float64      `json:"spent_usd"`
	Samples    int          `json:"samples"`
	Ledger     []Evidence   `json:"ledger"`
	Hypotheses []Hypothesis `json:"hypotheses,omitempty"`
}

// Run executes a multi-hypothesis sequential probability ratio test: it samples
// sources in decreasing evidence-per-dollar order and accumulates, for every
// distinct answer seen, the calibrated log-odds Λ_c that *that* answer is the
// correct one. It stops as soon as some answer's Λ_c crosses the Wald accept
// threshold A, or when budget or heads run out.
//
// Each source is evidence about every hypothesis, not only whichever answer
// happened to be current: a source whose answer matches c contributes
// LLR(agree) to c and LLR(disagree) to all the others. Nothing is discarded
// when the leader changes, and the hypothesis under test never moves, so
// Wald's error bound applies to a fixed comparison the way it is derived
// (#778). The previous binary form re-seeded Λ on a pivot, which both threw
// away the evidence gathered so far and continued against thresholds derived
// for the hypothesis it had just abandoned.
//
// Λ_c is the log-odds of "c is correct" against "c is incorrect", so σ(Λ_c) is
// that answer's probability of correctness and needs no normalizing across
// hypotheses. It also means two answers' confidences can sum past 1: they are
// each measured against their own negation rather than against each other,
// which is as far as a sensitivity/specificity parameterization reaches. A
// generator likelihood that makes them mutually exclusive needs the answer
// model in #771's follow-up, not a normalization here.
func Run(ctx context.Context, task Task, sources []Source, exec Executor, cal *Calibrator, t Target, opts ...RunOption) (*Result, error) {
	if len(sources) == 0 {
		return nil, fmt.Errorf("sprt: no sources provided")
	}
	cfg := runConfig{equiv: TextEquivalence}
	for _, o := range opts {
		o(&cfg)
	}
	alpha := 1 - t.Confidence
	if alpha <= 0 || alpha >= 1 {
		return nil, fmt.Errorf("sprt: confidence must be in (0,1), got %v", t.Confidence)
	}
	// An uncalibrated source has se=sp=0.5, so its verdict contributes exactly
	// ln(0.5/0.5)=0 nats however emphatically it agrees. With no calibrated
	// source for this domain no hypothesis can move, so the run samples every
	// head, reaches no threshold, exhausts its budget and reports the prior
	// back as its answer. Observed live: five heads agreed unanimously, every
	// LLR +0.000, "stopped_on_budget", confidence 50.0%, $0.0095 spent to learn
	// nothing (#698). Refusing costs the user nothing they would have got.
	if !cfg.allowUncalibrated && !anyEvidence(cal, sources, task.Domain) {
		return nil, fmt.Errorf("%w in domain %q", ErrNoEvidence, task.Domain)
	}
	A := math.Log((1 - alpha) / alpha)

	order := sampleOrder(sources, cal, task.Domain)

	res := &Result{Decision: DecisionStoppedOnBudget}
	var votes []vote
	familySeen := map[string]bool{}
	var seenIDs, seenFamilies, seenTexts []string

	for _, src := range order {
		cost := src.EstCostUSD
		if t.MaxCostUSD > 0 && res.SpentUSD+cost > t.MaxCostUSD {
			break // next sample would blow the budget
		}
		ans, err := exec.Execute(ctx, src, task)
		if err != nil {
			continue // a dead head is not evidence, skip it
		}
		if ans.CostUSD > 0 {
			cost = ans.CostUSD
		}
		res.SpentUSD += cost
		res.Samples++
		seenIDs = append(seenIDs, src.ID)
		seenFamilies = append(seenFamilies, src.Family)
		seenTexts = append(seenTexts, ans.Text)

		// A repeat vote from a family that has already spoken is discounted by
		// its measured excess agreement, not counted as fresh confirmation.
		weight := 1.0
		if src.Family != "" && familySeen[src.Family] {
			weight = FamilyDiscount(DefaultCoAgreementPath(), src.Family)
		}
		familySeen[src.Family] = true

		votes = append(votes, vote{src: src, text: ans.Text, weight: weight})
		hyps := scoreHypotheses(votes, cal, task.Domain, cfg.equiv)
		lead := hyps[0]

		agreed := cfg.equiv(lead.Answer, ans.Text)
		res.Ledger = append(res.Ledger, Evidence{
			Source:          src.ID,
			Agreed:          agreed,
			LLR:             weight * cal.LLR(src.ID, task.Domain, agreed),
			Candidate:       lead.Answer,
			LambdaAfter:     lead.Lambda,
			CostUSD:         cost,
			ConfidenceAfter: lead.Confidence,
		})

		res.Hypotheses = hyps
		res.Candidate = lead.Answer
		res.Lambda = lead.Lambda
		res.Confidence = lead.Confidence

		if lead.Lambda >= A {
			res.Decision = DecisionAccept
			break
		}
	}

	RecordCoAgreement(DefaultCoAgreementPath(), task.Domain, seenIDs, seenFamilies, seenTexts, cfg.equiv)
	return res, nil
}

// vote is one source's answer, with the correlation weight its contribution
// carries. Kept for the whole run because every hypothesis is scored against
// every vote, including votes cast before that hypothesis existed.
type vote struct {
	src    Source
	text   string
	weight float64
}

// scoreHypotheses scores every distinct answer against all votes so far,
// strongest first. Λ_c sums LLR(agree) over the votes matching c and
// LLR(disagree) over the rest, which is the log-odds that c is the correct
// answer. Recomputing from all votes each sample is O(votes × answers) on a
// handful of heads, and is what lets a hypothesis benefit from evidence
// gathered before anyone proposed it.
func scoreHypotheses(votes []vote, cal *Calibrator, domain string, equiv AnswerEquivalence) []Hypothesis {
	var hyps []Hypothesis
	for _, v := range votes {
		seen := false
		for _, h := range hyps {
			if equiv(h.Answer, v.text) {
				seen = true
				break
			}
		}
		if !seen {
			hyps = append(hyps, Hypothesis{Answer: v.text})
		}
	}
	for i := range hyps {
		var lambda float64
		votesFor := 0
		for _, v := range votes {
			match := equiv(hyps[i].Answer, v.text)
			if match {
				votesFor++
			}
			lambda += v.weight * cal.LLR(v.src.ID, domain, match)
		}
		hyps[i].Lambda = lambda
		hyps[i].Confidence = sigmoid(lambda)
		hyps[i].Votes = votesFor
	}
	// Ties break on the earlier-proposed answer, so a run is reproducible.
	sort.SliceStable(hyps, func(i, j int) bool { return hyps[i].Lambda > hyps[j].Lambda })
	return hyps
}

// sampleOrder ranks sources most-evidence-per-dollar first. The score is
// computed once per source (decorate-sort-undecorate), not inside the
// comparator: evidencePerCost takes the calibrator's lock and does two log()
// calls, so recomputing it per comparison turns an O(n) computation into
// O(n log n) lock acquisitions, real cost when Run is called thousands of
// times in a benchmark against a calibration that never changes mid-run.
func sampleOrder(sources []Source, cal *Calibrator, domain string) []Source {
	type scored struct {
		src   Source
		score float64
	}
	decorated := make([]scored, len(sources))
	for i, src := range sources {
		decorated[i] = scored{src: src, score: evidencePerCost(cal, src, domain)}
	}
	sort.SliceStable(decorated, func(i, j int) bool { return decorated[i].score > decorated[j].score })
	order := make([]Source, len(decorated))
	for i, d := range decorated {
		order[i] = d.src
	}
	return order
}

// evidencePerCost ranks sources by diagnostic power per dollar. Zero-cost
// sources (local) sort by raw D.
func evidencePerCost(cal *Calibrator, src Source, domain string) float64 {
	d := cal.D(src.ID, domain)
	if src.EstCostUSD <= 0 {
		return d * 1e6 // effectively free → sample first
	}
	return d / src.EstCostUSD
}

// AnswerEquivalence decides whether a source's answer counts as agreeing with the
// current candidate for the purpose of accumulating correctness evidence.
//
// The v1 default (TextEquivalence) compares normalized text, which miscounts two
// independently-correct answers that differ only in wording (variable names,
// println vs. fmt.Println) as *disagreement*, pushing Λ the wrong way. In the real
// benchmark this capped achieved confidence at 32.9% even though both sources were
// oracle-verified correct (see TRUST_CONTROL_PLANE_BENCHMARK_FINDINGS.md §3).
// Callers that can compare *behavior*, an oracle verdict in a benchmark, an LLM
// judge in production, inject a semantic comparator via WithEquivalence.
type AnswerEquivalence func(candidate, answer string) bool

// RunOption configures an SPRT run without changing Run's core signature.
type RunOption func(*runConfig)

type runConfig struct {
	equiv             AnswerEquivalence
	allowUncalibrated bool
}

// AllowNoEvidence runs the ensemble even when no source can move the
// posterior. For the benchmark harness, which runs precisely to produce the
// calibration that does not exist yet, and for tests asserting that an
// uninformative source contributes nothing. A caller routing real work wants
// the refusal.
func AllowNoEvidence() RunOption {
	return func(c *runConfig) { c.allowUncalibrated = true }
}

// ErrNoEvidence is returned when no source carries diagnostic power for the
// task's domain, either because none is calibrated there or because every one
// of them is measured as uninformative. Either way no verdict can move the
// posterior, so the run could only burn its budget and hand back the prior.
var ErrNoEvidence = errors.New("no source carries evidence")

// minD is the diagnostic power below which a source is treated as carrying no
// evidence. Calibration is stored as float rates, so an exactly-uninformative
// source lands near zero rather than on it.
const minD = 1e-9

// anyEvidence reports whether at least one source can contribute to the
// posterior in this domain.
func anyEvidence(cal *Calibrator, sources []Source, domain string) bool {
	for _, s := range sources {
		if cal.D(s.ID, domain) > minD {
			return true
		}
	}
	return false
}

// WithEquivalence overrides how answer agreement is decided (default:
// TextEquivalence). A nil comparator is ignored, keeping the default.
func WithEquivalence(fn AnswerEquivalence) RunOption {
	return func(c *runConfig) {
		if fn != nil {
			c.equiv = fn
		}
	}
}

// TextEquivalence is the v1 default agreement check: case-insensitive with
// collapsed whitespace. It removes trivial-formatting false disagreements but is
// NOT semantic, two behaviorally-equivalent answers with different identifiers
// still register as disagreement. Supply WithEquivalence for behavior-based
// comparison.
func TextEquivalence(candidate, answer string) bool {
	return normalizeAnswer(candidate) == normalizeAnswer(answer)
}

// normalizeAnswer canonicalizes text for TextEquivalence: lowercase, with runs of
// whitespace collapsed to single spaces and leading/trailing space removed.
func normalizeAnswer(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// sigmoid maps the log-odds Λ back to a probability for display.
func sigmoid(x float64) float64 { return 1 / (1 + math.Exp(-x)) }
