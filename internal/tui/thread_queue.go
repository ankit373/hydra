// SPDX-License-Identifier: MIT

package tui

// thread_queue.go — queue-on-overlap (#598): before an edit thread starts, its
// file target is checked against every other thread's holds — the same
// duplicate-target pre-flight internal/parallel runs, plus graph coupling.
// internal/a2a vector clocks are the apply-time backstop.

import (
	"fmt"
	"sort"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ankit373/hydra/internal/a2a"
)

// ckQueued is a task waiting for an overlap blocker to apply or discard.
type ckQueued struct {
	prompt    string
	mode      string // the chat mode at submit time — kept across the wait
	rel       string // the repo-relative target that overlapped
	seq       int    // queue position — earlier seq goes first, so two queued
	blockerID int    // threads can never block each other symmetrically
	reason    string // the visible chip/header line, path included
}

// heldFiles is what an edit thread currently holds against other threads: its
// running/gated/un-applied targets, plus a queued task's target (chains queue).
func (t *ckThread) heldFiles() []string {
	if t.queued != nil {
		if t.queued.rel != "" {
			return append(append([]string(nil), t.files...), t.queued.rel)
		}
		return t.files
	}
	if t.exec != nil || t.planWait != nil || t.confirm != nil || t.wt != nil || t.inEdit {
		return t.files
	}
	return nil
}

// overlapBlocker finds the thread that blocks self from editing rel ("" =
// no blocker). Degraded mode (no repo): edits are serial, whatever the file.
// A queued thread only blocks those behind it in the queue — never the other
// way round, so releases can never deadlock on symmetric holds.
func (m Cockpit) overlapBlocker(self *ckThread, rel string) (*ckThread, string) {
	selfSeq := int(^uint(0) >> 1) // a fresh submit queues behind everyone
	if self.queued != nil {
		selfSeq = self.queued.seq
	}
	for _, t := range m.threads {
		if t.id == self.id {
			continue
		}
		if t.queued != nil && t.queued.seq >= selfSeq {
			continue
		}
		if m.repoRoot == "" {
			// No isolation exists — one in-place edit at a time, full stop.
			if t.inEdit || t.queued != nil {
				return t, ""
			}
			continue
		}
		for _, f := range t.heldFiles() {
			if f == rel {
				return t, f
			}
			if m.metrics.graph != nil && m.metrics.graph.Coupled(f, rel) {
				return t, f
			}
		}
	}
	return nil, ""
}

// queueTask parks the prompt on the thread with its visible reason.
func (t *ckThread) queueTask(prompt, rel string, blocker *ckThread, overlap string) {
	reason := fmt.Sprintf("queued behind %d", blocker.id)
	switch {
	case overlap == rel && rel != "":
		reason += " · both touch " + rel
	case overlap != "":
		reason += fmt.Sprintf(" · %s is coupled to %s", rel, overlap)
	default:
		reason += " · no git repo — edits run one at a time"
	}
	t.queued = &ckQueued{prompt: prompt, rel: rel, blockerID: blocker.id, reason: reason}
	t.log = append(t.log, ckDimS.Render("  ◱ "+reason+" — auto-starts when it applies or discards"))
	if t.name == "" {
		t.name = ckThreadName(prompt)
	}
}

// releaseThreads re-checks every queued thread, oldest first, after releaser
// freed its holds — beginTask re-runs the gate and either starts or requeues.
// The releaser's clock merges into each waiter: whatever runs next causally
// follows what was just applied (#598).
func (m Cockpit) releaseThreads(releaser *ckThread) (Cockpit, tea.Cmd) {
	var waiting []*ckThread
	for _, t := range m.threads {
		if t.queued != nil {
			waiting = append(waiting, t)
		}
	}
	sort.Slice(waiting, func(i, j int) bool { return waiting[i].queued.seq < waiting[j].queued.seq })
	var cmds []tea.Cmd
	for _, t := range waiting {
		q := *t.queued
		if releaser != nil {
			t.clock = t.clock.Merge(releaser.clock)
		}
		var cmd tea.Cmd
		m, cmd = m.beginTask(t, q.prompt, q.mode)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	if len(cmds) == 0 {
		return m, nil
	}
	return m, tea.Batch(cmds...)
}

// requeueBehind updates the blocker/reason, echoing only on an actual change.
func (t *ckThread) requeueBehind(blocker *ckThread, overlap string) {
	if t.queued.blockerID == blocker.id {
		return
	}
	reason := fmt.Sprintf("queued behind %d", blocker.id)
	if overlap != "" {
		reason += " · both touch " + overlap
	}
	t.queued.blockerID, t.queued.reason = blocker.id, reason
	t.log = append(t.log, ckDimS.Render("  ◱ now "+reason))
}

// concurrentWarnings is the a2a backstop at apply time: any other thread whose
// handoff is causally concurrent AND overlaps this thread's files gets named
// before the merge — the queue should make this unreachable; the clocks catch
// what it missed (targets that only became known late).
func (m Cockpit) concurrentWarnings(self *ckThread) []string {
	h := &a2a.Handoff{From: ckThreadAgent(self.id), Files: self.files, Clock: self.clock}
	var out []string
	for _, t := range m.threads {
		if t.id == self.id || len(t.files) == 0 {
			continue
		}
		o := &a2a.Handoff{From: ckThreadAgent(t.id), Files: t.files, Clock: t.clock}
		if h.ConflictsWith(o) {
			out = append(out, ckMidS.Render("  ⚠ concurrent edit ")+ckDimS.Render(fmt.Sprintf(
				"thread %d also touched %s (vector clocks concurrent) — expect conflicts",
				t.id, ckOverlapPath(self.files, t.files))))
		}
	}
	return out
}

// ckOverlapPath names one file both sides touch, for the warning line.
func ckOverlapPath(a, b []string) string {
	set := make(map[string]bool, len(a))
	for _, f := range a {
		set[f] = true
	}
	for _, f := range b {
		if set[f] {
			return f
		}
	}
	return "the same files"
}
