// SPDX-License-Identifier: MIT

package runlog

import "strings"

// SubjectMax bounds what a run's subject costs on the line. A run event carries
// a short human label, never the full prompt: the atomic-append guarantee the
// log rests on is per write() call, so entries have to stay small.
const SubjectMax = 80

// DeclareRun records what a run is about, once, at its start.
//
// It is the only event that carries a run's subject, and it used to be written
// by `hyctl dispatch` and the TUI's chat and by nothing else, so a run opened by
// `hyctl workflow`, `hyctl edit` or `hyctl parallel` had no recorded subject at
// all. A reader then fell back to `task_started`, whose Detail is the routing
// enum, and listed a workflow as "GRUNT" (#910).
//
// Every command that opens a run calls this, so a new one cannot record the
// subject in its own shape or forget it quietly. An empty subject writes the
// event anyway: "this run started and said nothing about itself" is a fact a
// reader can render, and its absence is what let a routing key stand in.
func DeclareRun(runID, taskID, subject string) {
	_ = New(runID).Append(Event{
		Kind:   KindRunStarted,
		TaskID: taskID,
		Detail: Subject(subject),
	})
}

// FinishRun closes the run DeclareRun opened.
func FinishRun(runID, taskID string) {
	_ = New(runID).Append(Event{Kind: KindRunFinished, TaskID: taskID})
}

// Subject normalizes a run's subject to one line within SubjectMax runes.
// Cut on runes, not bytes, so a multi-byte character is never halved into
// replacement glyphs on the surface that renders it.
func Subject(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if r := []rune(s); len(r) > SubjectMax {
		return string(r[:SubjectMax]) + "…"
	}
	return s
}
