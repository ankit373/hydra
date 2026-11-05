// SPDX-License-Identifier: MIT

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/decide"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/serve"
	"github.com/ankit373/hydra/internal/trust"
)

// serveVerifiers builds each class's checks from [[serve.verifiers]]. The first
// bad entry stops startup by its position, so nothing binds on a check that cannot run.
func serveVerifiers(entries []config.ServeVerifier) (map[string][]decide.Verifier, error) {
	byClass := map[string][]decide.Verifier{}
	for i, e := range entries {
		class, v, err := serveVerifier(e)
		if err != nil {
			return nil, fmt.Errorf("[[serve.verifiers]] entry %d: %w", i+1, err)
		}
		for _, have := range byClass[class] {
			if have.Source == v.Source {
				return nil, fmt.Errorf("[[serve.verifiers]] entry %d: class %s already has %s", i+1, class, v.Source)
			}
		}
		byClass[class] = append(byClass[class], v)
	}
	return byClass, nil
}

func serveVerifier(e config.ServeVerifier) (string, decide.Verifier, error) {
	class, err := trust.TaskClass(e.Class)
	if err != nil {
		return "", decide.Verifier{}, err
	}
	if e.Kind != "match" {
		return "", decide.Verifier{}, fmt.Errorf("unknown kind %q; the one kind is match", e.Kind)
	}
	if strings.TrimSpace(e.ID) == "" {
		return "", decide.Verifier{}, errors.New("no id, which names the verifier's calibration key")
	}
	var labels []string
	if e.Labels != "" {
		if labels, err = readLabels(e.Labels); err != nil {
			return "", decide.Verifier{}, err
		}
	}
	o, err := oracle.NewMatch(e.ID, labels, e.Pattern)
	if err != nil {
		return "", decide.Verifier{}, err
	}
	return class, decide.Verifier{Source: o.Source, Oracle: o}, nil
}

// readLabels is one label per line; blank lines and '#' comments are skipped.
func readLabels(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("labels: %w", err)
	}
	defer f.Close()
	var labels []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" && !strings.HasPrefix(l, "#") {
			labels = append(labels, l)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("labels %s: %w", path, err)
	}
	if len(labels) == 0 {
		return nil, fmt.Errorf("labels %s holds no label", path)
	}
	return labels, nil
}

// escalation is one request's cascade and every answer it judged, in order.
type escalation struct {
	cascade *decide.Cascade
	results []*dispatch.Result
}

// escalationFor is set up only for a buffered request ordered by an objective whose
// class has a verifier; every other request dispatches exactly as before.
func (r serveRouter) escalationFor(ev *serve.Evidence, req serve.Request, opts *dispatch.Options) *escalation {
	if ev == nil || req.OnEvent != nil || opts.Objective == "" || len(r.verifiers[ev.TaskClass]) == 0 {
		return nil
	}
	e := &escalation{cascade: &decide.Cascade{
		Target:         trust.Target{Confidence: r.confidence, MaxCostUSD: cascadeCeiling(opts.MaxCostUSD)},
		MaxEscalations: r.maxEscalations,
		Cal:            r.cal,
		Domain:         ev.TaskClass,
		Verifiers:      r.verifiers[ev.TaskClass],
	}}
	// An answer escalated past, or served unaccepted, must never be cached.
	opts.Escalate, opts.NoCache = e.judge, true
	return e
}

func (e *escalation) judge(ctx context.Context, res *dispatch.Result) (bool, string) {
	e.results = append(e.results, res)
	next, reason := e.cascade.Judge(ctx, decide.Answer{
		Text:    res.Output,
		PPass:   float64(res.Scores[res.Head.ID].Effective) / 100,
		CostUSD: res.CostUSD,
	})
	// No objective means the quota governor routed this, and under its pressure escalations freeze.
	return next && res.Objective != "", reason
}

// served is the answer the cascade chose, which need not be the last one asked.
func (e *escalation) served() *dispatch.Result {
	return e.results[e.cascade.Outcome().Index]
}

// cascadeCeiling bounds what one request may spend across its escalations: the
// request's own ceiling, else policy.yaml's, since escalating is what multiplies spend.
func cascadeCeiling(requestUSD float64) float64 {
	if requestUSD > 0 {
		return requestUSD
	}
	usd, _ := servePolicyCeiling()
	return usd
}
