// SPDX-License-Identifier: MIT

package security

import (
	"fmt"
	"sort"

	"github.com/ankit373/hydra/internal/ledger"
)

// Did the rules change underneath the history?
//
// Every ledger event carries config.Breadcrumb() — a SHA256 over
// routing.yaml, models.yaml, domains.yaml and pricing.yaml as they were when
// the event was recorded. Because it is an ordinary field it is covered by the
// event's own hash, so a past event's breadcrumb cannot be rewritten without
// breaking the chain.
//
// That makes it the one durable answer to a question the rest of this report
// cannot ask: were these decisions all made under the same rules? A breadcrumb
// that changes partway through the log means the routing or pricing config was
// swapped mid-history — the local instance of the "rug pull" pattern, where an
// approval granted under one configuration silently continues to apply under a
// different one. It has been recorded on every event since #238 and read by
// nothing until now.

// ConfigEpoch is a span of the ledger recorded under one configuration.
type ConfigEpoch struct {
	// Breadcrumb is the deployment fingerprint, short-formed for display.
	Breadcrumb string `json:"breadcrumb"`
	Events     int    `json:"events"`
	FirstTS    string `json:"firstTs"`
	LastTS     string `json:"lastTs"`
}

// ConfigDrift reports whether the ledger spans more than one configuration.
type ConfigDrift struct {
	Epochs []ConfigEpoch `json:"epochs,omitempty"`
	// Changed is true when more than one distinct configuration produced the
	// events in this log.
	Changed bool `json:"changed"`
	// Unstamped counts events with no breadcrumb — recorded before the
	// fingerprint shipped, or by a build that could not read its registry.
	// Reported rather than folded into an epoch, because "unknown config" is
	// not a configuration.
	Unstamped int `json:"unstamped"`
}

// DetectConfigDrift groups events by the configuration in force when each was
// recorded, oldest epoch first.
func DetectConfigDrift(events []ledger.Event) ConfigDrift {
	type span struct {
		n           int
		first, last string
	}
	byCfg := map[string]*span{}
	var d ConfigDrift

	for _, e := range events {
		if e.Config == "" {
			d.Unstamped++
			continue
		}
		s, ok := byCfg[e.Config]
		if !ok {
			s = &span{first: e.TS}
			byCfg[e.Config] = s
		}
		s.n++
		s.last = e.TS
	}

	for cfg, s := range byCfg {
		d.Epochs = append(d.Epochs, ConfigEpoch{
			Breadcrumb: shortBreadcrumb(cfg), Events: s.n, FirstTS: s.first, LastTS: s.last,
		})
	}
	sort.Slice(d.Epochs, func(i, j int) bool { return d.Epochs[i].FirstTS < d.Epochs[j].FirstTS })
	d.Changed = len(d.Epochs) > 1
	return d
}

// shortBreadcrumb trims a SHA256 to a readable prefix. The full value is never
// needed for display — only for equality, which has already happened.
func shortBreadcrumb(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}

// driftCheck reports configuration changes across the recorded history.
func driftCheck(d ConfigDrift) Check {
	const name = "Configuration stability"
	switch {
	case len(d.Epochs) == 0:
		return Check{Name: name, Status: "no stamped events",
			Detail: "no event carries a configuration fingerprint, so rule changes cannot be detected"}
	case !d.Changed:
		detail := fmt.Sprintf("all %d stamped event(s) were recorded under one configuration (%s)",
			d.Epochs[0].Events, d.Epochs[0].Breadcrumb)
		if d.Unstamped > 0 {
			detail += fmt.Sprintf("; %d earlier event(s) carry no fingerprint", d.Unstamped)
		}
		return Check{Name: name, Status: "unchanged", Detail: detail}
	default:
		return Check{Name: name, Status: fmt.Sprintf("%d configurations", len(d.Epochs)),
			Detail: fmt.Sprintf("the routing/pricing config changed mid-history — decisions before %s "+
				"were made under different rules than those after it",
				d.Epochs[len(d.Epochs)-1].FirstTS)}
	}
}
