// SPDX-License-Identifier: MIT

package security

import (
	"fmt"
	"sort"

	"github.com/ankit373/hydra/internal/ledger"
)

// Were these decisions all made under the same rules? Each event's
// config.Breadcrumb is covered by its own hash, so a breadcrumb that changes
// mid-log is a config swapped underneath prior approvals — a local rug pull.

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
