// SPDX-License-Identifier: MIT

// Package waterfall reconstructs a run as nested spans on a timeline.
//
// This is a second reading of the same events internal/tree reads, not a
// replacement. A supervision tree answers "who owns what" and deliberately
// collapses a head's selection and its execution into one node. A waterfall
// answers "what happened when", where those are the start and end of one span
// and two attempts on the same head must stay apart. Keying the tree on span
// identity would break the first question to answer the second.
package waterfall

import (
	"sort"
	"time"

	"github.com/ankit373/hydra/internal/runlog"
)

// Span is one unit of work: the events sharing a span id, folded together.
type Span struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"`

	Kind   runlog.Kind  `json:"kind"`
	Level  runlog.Level `json:"level"`
	TaskID string       `json:"task_id,omitempty"`
	Agent  string       `json:"agent,omitempty"`
	Head   string       `json:"head,omitempty"`
	Model  string       `json:"model,omitempty"`
	Tier   int          `json:"tier,omitempty"`
	Status string       `json:"status,omitempty"`
	Detail string       `json:"detail,omitempty"`

	Start time.Time `json:"start"`
	End   time.Time `json:"end"`

	// DurationMS is what the executor reported, which is not the same as
	// End-Start: the span also covers policy checks and queueing around the
	// call. Both are kept because a gap between them is worth seeing.
	DurationMS int64 `json:"duration_ms,omitempty"`
	TTFTMs     int64 `json:"ttft_ms,omitempty"`

	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
	Confidence   float64 `json:"confidence,omitempty"`

	InputRef  string         `json:"input_ref,omitempty"`
	OutputRef string         `json:"output_ref,omitempty"`
	Meta      map[string]any `json:"meta,omitempty"`

	// Scores are verdicts on this span, appended after it closed. They never
	// touch the fields above: a judgement must not be able to rewrite what the
	// head actually reported.
	Scores []runlog.Score `json:"scores,omitempty"`

	Depth    int     `json:"depth"`
	Children []*Span `json:"children,omitempty"`
}

// Elapsed is the span's wall time on the timeline.
func (s *Span) Elapsed() time.Duration {
	if s.End.IsZero() || s.End.Before(s.Start) {
		return 0
	}
	return s.End.Sub(s.Start)
}

// Trace is one run's spans, nested.
type Trace struct {
	RunID string    `json:"run_id"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Roots []*Span   `json:"roots"`

	// Detail carries the run's own description, which lives on the run_started
	// event and belongs to no span.
	Detail string `json:"detail,omitempty"`

	// Unspanned counts events that named no span and could not be given one.
	// Surfaced rather than dropped: a partial trace rendered as a whole one is
	// the failure this package exists to avoid.
	Unspanned int `json:"unspanned,omitempty"`

	// OrphanScores counts verdicts naming a span this run does not contain.
	OrphanScores int `json:"orphan_scores,omitempty"`
}

// Build folds a run's events into a nested trace.
//
// Events are taken in the order given, which runlog.Load guarantees is append
// order. v1 events carry no span id; they are grouped by the identity a
// supervision tree would have used, so an old run still renders as spans rather
// than as one row per event.
func Build(events []runlog.Event) *Trace {
	t := &Trace{}
	if len(events) > 0 {
		t.RunID = events[0].RunID
	}

	byID := map[string]*Span{}
	seen := map[string]int{}
	scores := map[string][]runlog.Score{}
	var order []string
	for _, e := range events {
		if isRunLevel(e.Kind) {
			if e.Detail != "" && t.Detail == "" {
				t.Detail = e.Detail
			}
			continue
		}
		id := spanIDOf(e)
		if id == "" {
			t.Unspanned++
			continue
		}
		// A score is a verdict on a span, not part of one. Folding it in would
		// stretch the span's bar to whenever the test suite finished and
		// rename its kind, and a score alone must not mint a span with no work
		// in it, so they are collected and attached afterwards.
		if e.Kind == runlog.KindScore {
			if e.Score != nil {
				scores[id] = append(scores[id], *e.Score)
			}
			continue
		}
		s, ok := byID[id]
		if !ok {
			s = &Span{ID: id, ParentID: e.ParentSpan()}
			byID[id] = s
			order = append(order, id)
		}
		seen[id]++
		fold(s, e)
	}

	for id, sc := range scores {
		s, ok := byID[id]
		if !ok {
			// The span it judges is not in this run. Counted rather than
			// dropped: a verdict nobody can see is exactly what AppendScore
			// refuses to write, so one turning up here means the log is
			// partial and a reader should be told.
			t.OrphanScores += len(sc)
			continue
		}
		s.Scores = append(s.Scores, sc...)
	}

	for _, id := range order {
		s := byID[id]
		if s.End.IsZero() {
			s.End = s.Start
		}
		// A span logged as a single event has no start of its own, but a
		// reported duration says where it began. Swarm and SPRT write their
		// events after the work finishes, so without this their bars show when
		// the log was written rather than when the head ran.
		if seen[id] == 1 && s.DurationMS > 0 {
			s.Start = s.End.Add(-time.Duration(s.DurationMS) * time.Millisecond)
		}
	}
	t.Roots = nest(byID, order)
	t.Start, t.End = bounds(byID)
	return t
}

// isRunLevel reports events that describe the invocation rather than a unit of
// work inside it. They have no span and must not invent one.
func isRunLevel(k runlog.Kind) bool {
	return k == runlog.KindRunStarted || k == runlog.KindRunFinished
}

// spanIDOf is the event's span, or the identity a supervision tree would have
// keyed it under. The fallback exists for v1 events, which predate span ids;
// without it every event in an old run becomes its own row.
func spanIDOf(e runlog.Event) string {
	if e.SpanID != "" {
		return e.SpanID
	}
	switch {
	case e.Agent != "":
		return runlog.SpanIDFor(e.TaskID + "/" + e.Agent)
	case e.Head != "":
		return runlog.SpanIDFor(e.TaskID + "/" + e.Head)
	case e.TaskID != "":
		return runlog.SpanIDFor(e.TaskID)
	}
	return ""
}

// fold merges one event into its span. Later events win on the fields they
// carry, so a finish overwrites a selection's placeholder status, and fields a
// finish does not carry keep the value the selection put there.
func fold(s *Span, e runlog.Event) {
	ts := parseTS(e.TS)
	if !ts.IsZero() {
		if s.Start.IsZero() || ts.Before(s.Start) {
			s.Start = ts
		}
		if ts.After(s.End) {
			s.End = ts
		}
	}
	if s.ParentID == "" {
		s.ParentID = e.ParentSpan()
	}
	// The kind that closes a span describes it better than the one that opened
	// it, so a completed dispatch reads as finished rather than as selected.
	if s.Kind == "" || closes(e.Kind) {
		s.Kind = e.Kind
	}
	if lvl := e.Severity(); lvl != runlog.LevelInfo || s.Level == "" {
		s.Level = lvl
	}
	setIfEmpty(&s.TaskID, e.TaskID)
	setIfEmpty(&s.Agent, e.Agent)
	setIfEmpty(&s.Head, e.Head)
	setIfEmpty(&s.Model, e.Model)
	setIfEmpty(&s.Status, e.Status)
	setIfEmpty(&s.Detail, e.Detail)
	setIfEmpty(&s.InputRef, e.InputRef)
	setIfEmpty(&s.OutputRef, e.OutputRef)
	if e.Tier != 0 {
		s.Tier = e.Tier
	}
	if e.DurationMS != 0 {
		s.DurationMS = e.DurationMS
	}
	if e.TTFTMs != 0 {
		s.TTFTMs = e.TTFTMs
	}
	if e.InputTokens != 0 {
		s.InputTokens = e.InputTokens
	}
	if e.OutputTokens != 0 {
		s.OutputTokens = e.OutputTokens
	}
	if e.CostUSD != 0 {
		s.CostUSD = e.CostUSD
	}
	if e.Confidence != 0 {
		s.Confidence = e.Confidence
	}
	for k, v := range e.Meta {
		if s.Meta == nil {
			s.Meta = map[string]any{}
		}
		s.Meta[k] = v
	}
}

// closes reports the kinds that end a span rather than open one.
func closes(k runlog.Kind) bool {
	switch k {
	case runlog.KindDispatchFinished, runlog.KindTaskFinished, runlog.KindError,
		runlog.KindAttempt, runlog.KindSample, runlog.KindQuestionAsked:
		return true
	}
	return false
}

func setIfEmpty(dst *string, v string) {
	if *dst == "" {
		*dst = v
	}
}

// nest links children to parents and returns the roots, in first-seen order.
//
// A parent naming a span that no event declared is treated as absent rather
// than materialised: an invented node would render as a row with no head, no
// timing and no explanation.
func nest(byID map[string]*Span, order []string) []*Span {
	var roots []*Span
	for _, id := range order {
		s := byID[id]
		parent, ok := byID[s.ParentID]
		if !ok || s.ParentID == id {
			roots = append(roots, s)
			continue
		}
		parent.Children = append(parent.Children, s)
	}
	for _, id := range order {
		sortChildren(byID[id])
	}
	for _, r := range roots {
		setDepth(r, 0)
	}
	sort.SliceStable(roots, func(i, j int) bool { return roots[i].Start.Before(roots[j].Start) })
	return roots
}

func sortChildren(s *Span) {
	sort.SliceStable(s.Children, func(i, j int) bool {
		return s.Children[i].Start.Before(s.Children[j].Start)
	})
}

func setDepth(s *Span, d int) {
	s.Depth = d
	for _, c := range s.Children {
		setDepth(c, d+1)
	}
}

func bounds(byID map[string]*Span) (start, end time.Time) {
	for _, s := range byID {
		if s.Start.IsZero() {
			continue
		}
		if start.IsZero() || s.Start.Before(start) {
			start = s.Start
		}
		if s.End.After(end) {
			end = s.End
		}
	}
	return start, end
}

func parseTS(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if ts, err := time.Parse(layout, s); err == nil {
			return ts
		}
	}
	return time.Time{}
}

// Flatten returns the spans depth-first, which is the order a waterfall renders.
func (t *Trace) Flatten() []*Span {
	var out []*Span
	var walk func(s *Span)
	walk = func(s *Span) {
		out = append(out, s)
		for _, c := range s.Children {
			walk(c)
		}
	}
	for _, r := range t.Roots {
		walk(r)
	}
	return out
}

// Find returns the span with an id, or a unique prefix of one, so a reader can
// paste the first few characters off a waterfall row. An ambiguous prefix
// returns nothing rather than an arbitrary match.
func (t *Trace) Find(id string) *Span {
	var hit *Span
	for _, s := range t.Flatten() {
		if s.ID == id {
			return s
		}
		if len(id) > 0 && len(id) < len(s.ID) && s.ID[:len(id)] == id {
			if hit != nil {
				return nil
			}
			hit = s
		}
	}
	return hit
}

// Totals sums the trace's spend and usage.
func (t *Trace) Totals() (costUSD float64, inTok, outTok int) {
	for _, s := range t.Flatten() {
		costUSD += s.CostUSD
		inTok += s.InputTokens
		outTok += s.OutputTokens
	}
	return costUSD, inTok, outTok
}

// Verdict summarises a span's scores. Any non-positive score fails the span:
// aggregating the other way would let one passing check hide a failing one.
// known is false when nothing has judged this span, which is not the same as
// a pass and must not render as one.
func (s *Span) Verdict() (passed, known bool) {
	if len(s.Scores) == 0 {
		return false, false
	}
	for _, sc := range s.Scores {
		if sc.Value <= 0 {
			return false, true
		}
	}
	return true, true
}
