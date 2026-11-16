// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/entity"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/provider"
)

// HeadAsk sends one question to one named head. Not a dispatch: the head is
// the point, so routing would defeat it.
func HeadAsk(head provider.Head, maxTokens int) entity.Ask {
	exec := executor.For(head)
	return func(ctx context.Context, system, text string) (string, error) {
		resp, err := exec.Execute(ctx, entityRequest(head, maxTokens, system, text))
		if err != nil {
			return "", err
		}
		return resp.Output, nil
	}
}

// entityRequest pins greedy decoding, or the same head measures differently on
// every run and its verdict is a coin flip rather than evidence (#1041).
func entityRequest(head provider.Head, maxTokens int, system, text string) executor.Request {
	greedy := 0.0
	return executor.Request{
		Prompt: text, System: system, Head: head,
		MaxTokens: maxTokens, Temperature: &greedy,
	}
}

// NERReportPath is where `hyctl policy ner` keeps each head's measurement.
func NERReportPath() string { return filepath.Join(config.Dir(), "ner_reports.json") }

// nameKind is the placeholder kind for what the local pass marks: PERSONAL_1.
const nameKind = "personal"

// namePassTimeout bounds the local pass; past it the request is not sent.
const namePassTimeout = 2 * time.Minute

// namePass asks a local head measured eligible on this machine for the names
// and addresses in the request, and adds them to v (#1881). It returns what the
// receipt says about the pass; an error means the request must not be sent.
func (d *Dispatcher) namePass(ctx context.Context, texts []string, v *policy.Vault) (string, error) {
	h, ok := d.nameHead()
	if !ok {
		return "skipped: no local model measured eligible (hyctl policy ner)", nil
	}
	ctx, cancel := context.WithTimeout(ctx, namePassTimeout)
	defer cancel()
	found, err := entity.Spans(ctx, strings.Join(texts, "\n\n"), HeadAsk(h, 1024))
	if err != nil {
		return "", fmt.Errorf("the local name pass on %s failed, so nothing was sent: %w", h.ID, err)
	}
	v.AddValues(nameKind, found)
	return "ran on " + h.ID, nil
}

// nameHead is the first routable local head with an eligible saved report.
func (d *Dispatcher) nameHead() (provider.Head, bool) {
	reports, err := entity.LoadReports(NERReportPath())
	if err != nil {
		return provider.Head{}, false
	}
	for _, h := range d.heads {
		if h.LocalOnly && executor.Unroutable(h) == "" && reports[h.ID].Report.Eligible() {
			return h, true
		}
	}
	return provider.Head{}, false
}
