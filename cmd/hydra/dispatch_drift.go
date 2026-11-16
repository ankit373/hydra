// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/trust"
)

// driftWindow is how recent a drop or recovery must be to be named on a dispatch.
const driftWindow = 14 * 24 * time.Hour

// driftNotice is one line on the head that answered, and any head passed over
// on its record since a drop, when that record moved within driftWindow.
func driftNotice(stats []trust.Stat, r *dispatch.Result, domain string, now time.Time) string {
	if r.Domain != "" {
		domain = r.Domain // the cell the candidates were ranked for
	}
	cell := trust.Domain(domain)
	kind := trust.KindLabel(cell)
	recent := func(t time.Time) bool { return !t.IsZero() && now.Sub(t) <= driftWindow }
	day := func(t time.Time) string { return t.Local().Format("2 Jan") }
	var parts []string
	for _, s := range stats {
		if s.Domain != cell {
			continue
		}
		switch {
		case s.Source == r.Head.ID && recent(s.Recovered):
			parts = append(parts, fmt.Sprintf("%s record for %s recovered on %s", kind, s.Source, day(s.Recovered)))
		case s.Source == r.Head.ID && s.Recovered.IsZero() && recent(s.Changed):
			parts = append(parts, fmt.Sprintf("%s record for %s changed on %s", kind, s.Source, day(s.Changed)))
		case s.Source != r.Head.ID && s.Recovered.IsZero() && recent(s.Changed):
			// Scored but not clearing is what passing over on the record looks like.
			if sc, ok := r.Scores[s.Source]; ok && !sc.Clears {
				parts = append(parts, fmt.Sprintf("passed over %s, its %s record changed on %s", s.Source, kind, day(s.Changed)))
			}
		}
	}
	return strings.Join(parts, "; ")
}

// printDriftNotice loads calibration and prints driftNotice dim, or nothing.
func printDriftNotice(r *dispatch.Result, domain string) {
	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		return
	}
	if line := driftNotice(cal.Report(), r, domain, time.Now()); line != "" {
		fmt.Fprintf(stdout, "  %s\n", dimStyle.Render(line))
	}
}
