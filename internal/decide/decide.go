// SPDX-License-Identifier: MIT

// Package decide routes one decision across decision heads: it asks the first,
// returns if that answer is sure enough, and otherwise combines more through the SPRT.
package decide

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ankit373/hydra/internal/decision"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/trust"
)

// Asker asks one decision head; executor.DecisionExecutor is the production one.
type Asker interface {
	Execute(ctx context.Context, h provider.Head, r decision.Request) (decision.Result, error)
}

// Router asks its Heads in the order given, which is the declared order until
// calibration can rank them.
type Router struct {
	Heads []provider.Head
	// Price estimates one call to h for r, so the budget is checked before paying.
	Price  func(h provider.Head, r decision.Request) float64
	Ask    Asker
	Cal    *trust.Calibrator
	Target trust.Target
}

// Call is one decision to route.
type Call struct {
	Request decision.Request
	// Domain is the calibration domain the sources are weighed in.
	Domain string
	// RunID is the run each source's answer is logged under.
	RunID string
}

// Outcome is a routed decision and the evidence behind it.
type Outcome struct {
	Result decision.Result
	// Asked is every source that answered, in the order it was asked.
	Asked []string
	// Confidence is the first source's own when it sufficed, else the SPRT posterior.
	Confidence float64
	Decision   Decision
}

// ErrNoSource means no source within budget answered.
var ErrNoSource = errors.New("no decision source answered")

// Decide asks the first source that answers. Its answer stands when every
// question's top probability meets the target; otherwise the SPRT asks on.
func (r Router) Decide(ctx context.Context, c Call) (Outcome, error) {
	c.Domain = trust.Domain(c.Domain)
	asked := &askLog{
		router:  r,
		call:    c,
		results: map[string]decision.Result{},
		failed:  map[string]error{},
		byKey:   map[string]decision.Result{},
		heads:   make(map[string]provider.Head, len(r.Heads)),
	}
	sources := make([]trust.Source, len(r.Heads))
	for i, h := range r.Heads {
		sources[i] = trust.Source{ID: h.ID, Family: h.Provider, EstCostUSD: r.Price(h, c.Request)}
		asked.heads[h.ID] = h
	}
	paid, first, err := asked.first(ctx, sources)
	if err != nil {
		return Outcome{}, err
	}
	stated := statedConfidence(first)
	alone := Outcome{Result: first, Asked: asked.order, Confidence: stated, Decision: BudgetExhausted}
	if stated >= r.Target.Confidence {
		alone.Decision = Accepted
		return alone, nil
	}
	if len(r.Heads) == 1 {
		return alone, nil
	}

	rest, target := afterFirst(sources, paid, r.Target)
	res, err := trust.Run(ctx, trust.Task{Domain: c.Domain}, rest, asked, r.Cal, target,
		trust.WithEquivalence(sameAnswer))
	// With nothing calibrated no further answer can move the posterior, so asking would only spend.
	if errors.Is(err, trust.ErrNoEvidence) {
		alone.Decision = NoEvidence
		return alone, nil
	}
	if err != nil {
		return Outcome{}, err
	}
	// The SPRT stops before any sample when the source it ranks first is past the budget.
	accepted, ok := asked.byKey[res.Candidate]
	if res.Samples == 0 || !ok {
		return alone, nil
	}
	d := BudgetExhausted
	if res.Decision == trust.DecisionAccept {
		d = Accepted
	}
	return Outcome{Result: accepted, Asked: asked.order, Confidence: res.Confidence, Decision: d}, nil
}

// afterFirst prices the SPRT's run once paid has answered: its answer costs nothing
// more, and the ceiling shrinks by what it cost.
func afterFirst(sources []trust.Source, paid trust.Source, t trust.Target) ([]trust.Source, trust.Target) {
	rest := make([]trust.Source, len(sources))
	for i, s := range sources {
		if s.ID == paid.ID {
			s.EstCostUSD = 0
		}
		rest[i] = s
	}
	if t.MaxCostUSD <= 0 {
		return rest, t
	}
	t.MaxCostUSD -= paid.EstCostUSD
	if t.MaxCostUSD > 0 {
		return rest, t
	}
	// Nothing is left to spend, and a ceiling of 0 means none, so only free sources stay.
	t.MaxCostUSD = 0
	return slices.DeleteFunc(rest, func(s trust.Source) bool { return s.EstCostUSD > 0 }), t
}

// sameAnswer is exact: answers are keys built from option names, which differ
// only when the answers do, so trust's case-folding comparison would merge two.
func sameAnswer(a, b string) bool { return a == b }

// key is a result's top outcome per question, the hypothesis the SPRT tests.
func (c Call) key(res decision.Result) string {
	tops := make([]string, len(c.Request.Questions))
	for i, q := range c.Request.Questions {
		tops[i] = q.Outcomes()[res.Answers[i].Top()].Name
	}
	return strings.Join(tops, "\x1f")
}

// statedConfidence is the least sure of a result's answers, since every one has to hold.
func statedConfidence(res decision.Result) float64 {
	least := 1.0
	for _, a := range res.Answers {
		least = min(least, a.Probabilities[a.Top()])
	}
	return least
}

// askLog asks each source at most once per call, so the first answer is reused
// when the SPRT reaches that source rather than paid for twice.
type askLog struct {
	router  Router
	call    Call
	results map[string]decision.Result
	// failed keeps each error, so a source that failed once is not paid to fail again.
	failed map[string]error
	// byKey is the first result giving each answer, which is what an accepted answer returns.
	byKey map[string]decision.Result
	heads map[string]provider.Head
	order []string
}

// first asks sources in order until one within budget answers, and returns which one did.
func (l *askLog) first(ctx context.Context, sources []trust.Source) (trust.Source, decision.Result, error) {
	budget := l.router.Target.MaxCostUSD
	var errs []error
	for _, s := range sources {
		if budget > 0 && s.EstCostUSD > budget {
			continue
		}
		res, err := l.ask(ctx, l.heads[s.ID])
		if err == nil {
			return s, res, nil
		}
		errs = append(errs, err)
	}
	err := fmt.Errorf("%w: none is configured within budget", ErrNoSource)
	if len(errs) > 0 {
		err = fmt.Errorf("%w: %w", ErrNoSource, errors.Join(errs...))
	}
	return trust.Source{}, decision.Result{}, err
}

// Execute is the SPRT's view of a source: its answer as the key under test.
func (l *askLog) Execute(ctx context.Context, src trust.Source, _ trust.Task) (trust.Answer, error) {
	res, err := l.ask(ctx, l.heads[src.ID])
	if err != nil {
		return trust.Answer{}, err
	}
	return trust.Answer{Text: l.call.key(res)}, nil
}

func (l *askLog) ask(ctx context.Context, h provider.Head) (decision.Result, error) {
	if res, ok := l.results[h.ID]; ok {
		return res, nil
	}
	if err, ok := l.failed[h.ID]; ok {
		return decision.Result{}, err
	}
	res, err := l.router.Ask.Execute(ctx, h, l.call.Request)
	if err != nil {
		l.failed[h.ID] = err
		return decision.Result{}, err
	}
	l.results[h.ID] = res
	l.order = append(l.order, h.ID)
	key := l.call.key(res)
	if _, seen := l.byKey[key]; !seen {
		l.byKey[key] = res
	}
	l.record(h, res)
	return res, nil
}

// record logs each answer's stated probability, so a later verdict can audit
// whether this source's numbers are honest. A lost event never fails the call.
func (l *askLog) record(h provider.Head, res decision.Result) {
	log := runlog.New(l.call.RunID)
	for i, q := range l.call.Request.Questions {
		a := res.Answers[i]
		// A predicate's forecast is P(true); any other is the probability of the chosen outcome.
		stated := a.Probabilities[a.Top()]
		if q.Kind == decision.Predicate {
			stated = a.Probabilities[0]
		}
		_ = log.Append(runlog.Event{
			Kind:       runlog.KindDecision,
			SpanID:     runlog.NewSpanID(),
			Head:       h.ID,
			Model:      res.Model,
			Confidence: stated,
			Domain:     l.call.Domain,
			Detail:     q.Name,
		})
	}
}
