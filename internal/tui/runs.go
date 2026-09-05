// SPDX-License-Identifier: MIT

package tui

// runs.go, the run-list data layer shared by the agents and activity views:
// loading today's runs from the run log and folding each event stream into a
// status the header counts can trust (running is never counted as ok).

import (
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/runlog"
)

type ckRun struct {
	id      string
	live    bool
	status  string // "ok" | "failed" | "running"
	task    string
	start   time.Time
	durMS   int64
	costUSD float64
	fails   int
	edited  []string
	events  []runlog.Event
}

// ckLoadRuns reads today's runs (plus any still-live one, whatever day it
// started) from the run log, newest first.
func ckLoadRuns(now time.Time) []ckRun {
	ids, err := runlog.Runs()
	if err != nil {
		return nil
	}
	liveIDs, _ := runlog.LiveRuns()
	live := map[string]bool{}
	for _, id := range liveIDs {
		live[id] = true
	}
	day := now.Format("20060102")
	dayISO := now.Format("2006-01-02")

	var out []ckRun
	for _, id := range ids {
		// Cheap date gate on runid.New's "YYYYMMDDT…" prefix before paying for
		// a file read; arbitrary external ids fall through to the event check.
		if !live[id] && len(id) > 8 && id[8] == 'T' && !strings.HasPrefix(id, day) {
			if _, perr := time.Parse("20060102", id[:8]); perr == nil {
				continue
			}
		}
		events, lerr := runlog.Load(id)
		if lerr != nil || len(events) == 0 {
			continue
		}
		r := ckRunFromEvents(id, events, live[id])
		if !r.live && !strings.HasPrefix(events[0].TS, dayISO) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// ckRunFromEvents folds one run's event stream into a list row. Status is
// derived, never assumed: running needs a live heartbeat, ok needs either a
// successful completion or a clean finish with zero errors.
func ckRunFromEvents(id string, events []runlog.Event, live bool) ckRun {
	r := ckRun{id: id, live: live, events: events}
	var hasOK, finished bool
	var start, end time.Time
	var dispCost, attemptCost float64
	for _, e := range events {
		if t, err := time.Parse(time.RFC3339Nano, e.TS); err == nil {
			if start.IsZero() {
				start = t
			}
			end = t
		}
		switch e.Kind {
		case runlog.KindRunStarted, runlog.KindTaskStarted:
			if r.task == "" && e.Detail != "" {
				r.task = e.Detail
			}
		case runlog.KindRunFinished:
			finished = true
		case runlog.KindDispatchFinished:
			if e.Status == "ok" {
				hasOK = true
			}
			dispCost += e.CostUSD
		case runlog.KindAttempt:
			if e.Status == "ok" {
				hasOK = true
			}
			attemptCost += e.CostUSD
		case runlog.KindEdit:
			if e.File != "" {
				r.edited = append(r.edited, e.File)
			}
		case runlog.KindError:
			r.fails++
		}
	}
	// Swarm attempts and their dispatch rows describe the same spend, take
	// one, never the sum of both.
	r.costUSD = dispCost
	if r.costUSD == 0 {
		r.costUSD = attemptCost
	}
	r.start = start
	if !start.IsZero() && end.After(start) {
		r.durMS = end.Sub(start).Milliseconds()
	}
	switch {
	case live:
		r.status = "running"
	case hasOK:
		r.status = "ok"
	case r.fails > 0:
		r.status = "failed"
	case finished:
		r.status = "ok"
	default:
		r.status = "failed" // never finished, no heartbeat, interrupted
	}
	return r
}

// runCostUSD is the one figure both a list row and the trace's done row show:
// runlog-recorded cost, else the cost.jsonl join, never both summed.
func (m Cockpit) runCostUSD(r ckRun) float64 {
	if r.costUSD > 0 {
		return r.costUSD
	}
	return m.metrics.runCost[r.id].costUSD
}

// activityRuns is today's run list, optionally failures-only.
func (m Cockpit) activityRuns() []ckRun {
	if !m.actFailOnly {
		return m.runsToday
	}
	var out []ckRun
	for _, r := range m.runsToday {
		if r.status == "failed" {
			out = append(out, r)
		}
	}
	return out
}

// agentRows is the agents view's list: live runs first, then today's finished.
func (m Cockpit) agentRows() []ckRun {
	var live, done []ckRun
	for _, r := range m.runsToday {
		if r.live {
			live = append(live, r)
		} else {
			done = append(done, r)
		}
	}
	return append(live, done...)
}

func (m Cockpit) agentCounts() (live, done int) {
	for _, r := range m.runsToday {
		if r.live {
			live++
		} else {
			done++
		}
	}
	return live, done
}

// ckRunCounts tallies a list so headers stay consistent, a running run is
// counted as running, never as ok.
func ckRunCounts(runs []ckRun) (ok, failed, running int) {
	for _, r := range runs {
		switch r.status {
		case "ok":
			ok++
		case "running":
			running++
		default:
			failed++
		}
	}
	return ok, failed, running
}

func ckStatusGlyph(status string) string {
	switch status {
	case "ok":
		return ckCheapS.Render("✓")
	case "running":
		return ckAquaS.Render("⠸")
	default:
		return ckExpS.Render("✗")
	}
}

// ckShortID is the tail of a run id, the hex part is what tells runs apart;
// the timestamp prefix is already visible as the list's order.
func ckShortID(id string) string {
	if i := strings.LastIndex(id, "-"); i >= 0 && i+1 < len(id) {
		return truncate(id[i+1:], 8)
	}
	return truncate(id, 8)
}
