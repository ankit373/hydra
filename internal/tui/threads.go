// SPDX-License-Identifier: MIT

package tui

// threads.go — parallel chat threads (#598): per-thread task state, the chip
// strip, switching/attention keys, and split bookkeeping. Worktree isolation
// lives in thread_worktree.go, queue-on-overlap in thread_queue.go.

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/ankit373/hydra/internal/a2a"
)

// ckMaxThreads bounds the strip and keeps every thread reachable by alt+1–9.
const ckMaxThreads = 9

// ckThread is one chat thread: its own scrollback, input draft, task pipeline,
// code panel, and (for edit work) worktree + file holds. Threads are held by
// pointer so a tea.Cmd result can land on the right one whatever is current.
type ckThread struct {
	id   int    // stable 1-based id — the chip number and the alt+N target
	name string // short chip label, from the first prompt

	input  string
	log    []string
	scroll int // 0 = live tail; L+1 = anchored at rendered line L

	exec     *ckExecState
	planWait *ckWait
	confirm  *ckWait
	lastDone *ckTask
	attn     bool // finished needing eyes (a failure) — cleared on visit

	bg         bool        // re-parented to the agents view
	queued     *ckQueued   // waiting on an overlap blocker; nil otherwise
	wt         *ckWorktree // edit isolation; nil = shared tree
	files      []string    // repo-relative edit targets this thread holds
	clock      a2a.Clock   // causal backstop across threads
	inEdit     bool        // an in-place (no-isolation) edit task is running
	discardArm bool        // first x pressed; the next x discards the worktree
	lastRunID  string      // latest task's run id — the agents-view join key

	// code panel — per thread, each shows its own last edit
	codeLang  string
	codeLines []string
	codeShown int
	codeGen   int
	codeDiff  bool
}

func newThread(id int) *ckThread {
	return &ckThread{id: id, clock: a2a.Clock{}.Tick(ckThreadAgent(id))}
}

func ckThreadAgent(id int) string { return fmt.Sprintf("thread-%d", id) }

// status is the chip state: queued < running < needs < done < idle, evaluated
// in gate order — a queued thread is queued even though nothing executes yet.
func (t *ckThread) status() string {
	switch {
	case t.queued != nil:
		return "queued"
	case t.exec != nil:
		return "running"
	case t.planWait != nil || t.confirm != nil || t.attn:
		return "needs"
	case t.lastDone != nil:
		return "done"
	default:
		return "idle"
	}
}

// ckThreadGlyph renders a status as its strip glyph.
func ckThreadGlyph(status string) string {
	switch status {
	case "running":
		return ckAquaS.Render("⠸")
	case "needs":
		return ckMidS.Render("●")
	case "queued":
		return ckDimS.Render("◱")
	case "done":
		return ckCheapS.Render("✓")
	default:
		return ckFaintS.Render("○")
	}
}

// ── registry ──────────────────────────────────────────────────────────────────

// withThreads guarantees thread 1 exists — a bare Cockpit{} must render and
// route keys without panicking, exactly like the pre-thread model did.
func (m Cockpit) withThreads() Cockpit {
	if len(m.threads) == 0 {
		m.threads = []*ckThread{newThread(1)}
		m.cur, m.nextID = 0, 2
	}
	return m
}

// th is the active thread. Every entry point runs withThreads first, so the
// index is always valid.
func (m Cockpit) th() *ckThread { return m.threads[m.cur] }

func (m Cockpit) threadByID(id int) *ckThread {
	for _, t := range m.threads {
		if t.id == id {
			return t
		}
	}
	return nil
}

// visibleThreads are the strip's chips — backgrounded threads live in the
// agents view instead.
func (m Cockpit) visibleThreads() []*ckThread {
	var out []*ckThread
	for _, t := range m.threads {
		if !t.bg {
			out = append(out, t)
		}
	}
	return out
}

// threadCounts is the attention tally over every thread, backgrounded included.
func (m Cockpit) threadCounts() (running, needs, queued int) {
	for _, t := range m.threads {
		switch t.status() {
		case "running":
			running++
		case "needs":
			needs++
		case "queued":
			queued++
		}
	}
	return
}

// ── switching ─────────────────────────────────────────────────────────────────

// addThread opens a fresh thread and makes it current. Capped so alt+1–9 can
// always address every thread; the cap refuses visibly, never silently.
func (m Cockpit) addThread() Cockpit {
	if len(m.threads) >= ckMaxThreads {
		m.flash = fmt.Sprintf("thread limit %d — apply or reuse an existing one", ckMaxThreads)
		return m
	}
	t := newThread(m.nextID)
	t.log = []string{ckDimS.Render(fmt.Sprintf("thread %d — type a task and press enter.", t.id))}
	m.nextID++
	m.threads = append(m.threads, t)
	return m.focusThread(t.id)
}

// focusThread makes thread id current (foregrounding it if backgrounded),
// clears its attention mark, and lands on the chat view.
func (m Cockpit) focusThread(id int) Cockpit {
	t := m.threadByID(id)
	if t == nil {
		m.flash = fmt.Sprintf("no thread %d", id)
		return m
	}
	if t.bg {
		t.bg = false
	}
	t.attn = false
	t.discardArm = false
	for i, o := range m.threads {
		if o.id == id {
			if m.cur != i {
				m.prevCur = m.threads[m.cur].id
			}
			m.cur = i
			break
		}
	}
	// jump also closes the transient overlays, wherever the key was pressed.
	m = m.jump(ckViewChat)
	// A split showing the same thread twice says nothing — drop the pin.
	if m.split && m.splitID == t.id {
		m.split = false
	}
	return m
}

// cycleThread moves to the next/previous visible thread; under a split it
// moves focus to the other pane instead (the pinned side becomes active).
func (m Cockpit) cycleThread(dir int) Cockpit {
	if m.split {
		other := m.threadByID(m.splitID)
		if other != nil {
			curID := m.th().id
			m = m.focusThread(other.id)
			// focusThread drops a self-pin; re-pin the previous side.
			m.split, m.splitID = true, curID
			return m
		}
	}
	vis := m.visibleThreads()
	if len(vis) < 2 {
		m.flash = "one thread — ctrl+t opens another"
		return m
	}
	curID := m.th().id
	idx := 0
	for i, t := range vis {
		if t.id == curID {
			idx = i
			break
		}
	}
	next := vis[((idx+dir)%len(vis)+len(vis))%len(vis)]
	return m.focusThread(next.id)
}

// nextAttention jumps to the next thread needing input (gates, failures) —
// backgrounded threads included, since their pings point here.
func (m Cockpit) nextAttention() Cockpit {
	n := len(m.threads)
	for off := 1; off <= n; off++ {
		t := m.threads[(m.cur+off)%n]
		if t.status() == "needs" {
			return m.focusThread(t.id)
		}
	}
	m.flash = "nothing needs you"
	return m
}

// ── split ─────────────────────────────────────────────────────────────────────

// ckSplitMinCols is the narrowest terminal a split renders in; below it the
// toggle refuses with a note instead of drawing two broken panes.
const ckSplitMinCols = 100

// toggleSplit pins a second thread beside the active one (one split max).
func (m Cockpit) toggleSplit() Cockpit {
	if m.split {
		m.split = false
		return m
	}
	if m.w < ckSplitMinCols {
		m.flash = fmt.Sprintf("split needs ≥%d cols (terminal is %d)", ckSplitMinCols, m.w)
		return m
	}
	pin := m.pickSplitPin()
	if pin == 0 {
		m.flash = "no second thread to pin — ctrl+t opens one"
		return m
	}
	m.split, m.splitID = true, pin
	return m
}

// pickSplitPin chooses the pinned side: the previously active thread when it
// is still visible, else the next visible one. 0 = nothing to pin.
func (m Cockpit) pickSplitPin() int {
	cur := m.th().id
	if t := m.threadByID(m.prevCur); t != nil && !t.bg && t.id != cur {
		return t.id
	}
	for _, t := range m.visibleThreads() {
		if t.id != cur {
			return t.id
		}
	}
	return 0
}

// dropSplitIfNarrow disables an active split when the terminal shrinks below
// the split minimum — one honest pane instead of two garbled ones.
func (m Cockpit) dropSplitIfNarrow() Cockpit {
	if m.split && m.w < ckSplitMinCols {
		m.split = false
		m.flash = fmt.Sprintf("split closed — needs ≥%d cols", ckSplitMinCols)
	}
	return m
}

// ── strip ─────────────────────────────────────────────────────────────────────

// showStrip: the strip earns its line only once threads exist to switch between.
func (m Cockpit) showStrip() bool { return len(m.visibleThreads()) > 1 }

// threadStrip renders the chip row: number · status glyph · short name ·
// worktree tag. Overflow windows around the active chip with ‹/› cues.
func (m Cockpit) threadStrip(w int) string {
	vis := m.visibleThreads()
	chips := make([]string, len(vis))
	active := 0
	for i, t := range vis {
		if t.id == m.th().id {
			active = i
		}
		chips[i] = m.threadChip(t)
	}
	sep := ckFaintS.Render(" │ ")
	row := " " + strings.Join(chips, sep)
	if lipgloss.Width(row) <= w {
		return ckFrame(row, w, 1)
	}
	// Window around the active chip, widening while it fits.
	lo, hi := active, active
	for {
		grown := false
		if hi+1 < len(chips) && ckStripFits(chips, lo, hi+1, sep, lo > 0, true, w) {
			hi++
			grown = true
		}
		if lo > 0 && ckStripFits(chips, lo-1, hi, sep, true, hi < len(chips)-1, w) {
			lo--
			grown = true
		}
		if !grown {
			break
		}
	}
	parts := make([]string, 0, hi-lo+3)
	if lo > 0 {
		parts = append(parts, ckFaintS.Render(fmt.Sprintf("‹%d", lo)))
	}
	parts = append(parts, chips[lo:hi+1]...)
	if hi < len(chips)-1 {
		parts = append(parts, ckFaintS.Render(fmt.Sprintf("%d›", len(chips)-1-hi)))
	}
	return ckFrame(" "+strings.Join(parts, sep), w, 1)
}

// ckStripFits reports whether chips[lo..hi] plus overflow cues fit in w cells.
func ckStripFits(chips []string, lo, hi int, sep string, cueL, cueR bool, w int) bool {
	total := 1
	for i := lo; i <= hi; i++ {
		if i > lo {
			total += lipgloss.Width(sep)
		}
		total += lipgloss.Width(chips[i])
	}
	if cueL {
		total += lipgloss.Width(sep) + 3
	}
	if cueR {
		total += lipgloss.Width(sep) + 3
	}
	return total <= w
}

// threadChip renders one strip chip.
func (m Cockpit) threadChip(t *ckThread) string {
	name := t.name
	if name == "" {
		name = "new"
	}
	label := fmt.Sprintf("%d", t.id)
	body := " " + truncate(name, 14)
	if t.wt != nil {
		body += ckVioletS.Render(" ·wt")
	}
	if t.id == m.th().id {
		return ckChipS.Render(label) + ckThreadGlyph(t.status()) + ckInkS.Bold(true).Render(body)
	}
	return ckDimS.Render(label) + ckThreadGlyph(t.status()) + ckDimS.Render(body)
}

// splitPanes renders the active thread beside the pinned one: each side owns
// its own scrollback; the shared input bar under both targets the active side.
func (m Cockpit) splitPanes(mainW, bodyH int) string {
	input := m.inputBar(mainW)
	paneH := max(1, bodyH-lipgloss.Height(input))
	paneW := max(10, (mainW-1)/2)
	active := m.threadPane(m.th(), paneW, paneH, true)
	pinned := m.threadPane(m.threadByID(m.splitID), paneW, paneH, false)
	return lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.JoinHorizontal(lipgloss.Top, active, " ", pinned), input)
}

// threadPane is one split side: a title row, the thread's context header when
// it has one, and its scrollback window.
func (m Cockpit) threadPane(t *ckThread, w, h int, active bool) string {
	name := t.name
	if name == "" {
		name = "new"
	}
	title := fmt.Sprintf("thread %d · %s", t.id, name)
	style, mark := ckDimS, "  "
	if active {
		style, mark = ckAquaS, "▸ "
	} else {
		title += " · watching"
	}
	rows := []string{ckFrame(style.Render(mark+title), w, 1)}
	logH := h - 1
	if hdr := t.threadHeaderLine(w); hdr != "" {
		rows = append(rows, hdr)
		logH--
	}
	logH = max(1, logH)
	rows = append(rows, lipgloss.NewStyle().Width(w).Height(logH).
		Render(ckVisibleLog(t.log, w, logH, t.scroll-1)))
	return lipgloss.NewStyle().Width(w).Height(h).Render(strings.Join(rows, "\n"))
}

// threadHeaderLine is the one-line context header above a thread's log: its
// isolation state, or why it is queued. Empty when there is nothing to say.
func (t *ckThread) threadHeaderLine(w int) string {
	switch {
	case t.queued != nil:
		return ckFrame(ckDimS.Render(" ◱ "+t.queued.reason), w, 1)
	case t.wt != nil:
		return ckFrame(ckDimS.Render(" ⎇ worktree ")+ckVioletS.Render(t.wt.tag)+
			ckDimS.Render(" · merges on apply"), w, 1)
	}
	return ""
}

// ckThreadName derives the chip label from the first prompt.
func ckThreadName(prompt string) string {
	return truncate(ckSafe(strings.TrimSpace(prompt)), 14)
}

// attentionFact is the status-bar tally: "⠸ 2 running · ● 1 needs you · ◱ 1 queued".
func (m Cockpit) attentionFact() string {
	running, needs, queued := m.threadCounts()
	var parts []string
	if running > 0 {
		parts = append(parts, fmt.Sprintf("⠸ %d running", running))
	}
	if needs > 0 {
		parts = append(parts, fmt.Sprintf("● %d needs you", needs))
	}
	if queued > 0 {
		parts = append(parts, fmt.Sprintf("◱ %d queued", queued))
	}
	return strings.Join(parts, " · ")
}
