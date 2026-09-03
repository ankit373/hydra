// SPDX-License-Identifier: MIT

package tui

// keys.go — every binding, declared once in ckKeymap and routed here. The
// glossary and the status bars render FROM the table, so keys and their docs
// cannot drift; only the behavior lives in the switches below.

import (
	tea "github.com/charmbracelet/bubbletea"
)

// ckBinding is one documented key: what it does, which glossary group it is
// explained under ("" = bar-only refinement of a generic group row), and which
// views' status bars surface it.
type ckBinding struct {
	keys  string
	does  string
	group string // EVERYWHERE / CHAT / LISTS / "" (bar-only)
	views []int  // status bars that show it; nil = glossary only
}

var ckKeymap = []ckBinding{
	{"tab", "next view", "EVERYWHERE", nil},
	{"1–6", "jump to a view (in chat, digits type into the input)", "EVERYWHERE", nil},
	{"?", "this glossary (in chat: when the input is empty)", "EVERYWHERE", nil},
	{"esc", "close overlay · back · clear chat input", "EVERYWHERE", nil},
	{"pgup/pgdn · wheel", "scroll · lists: page the selection", "EVERYWHERE", nil},
	{"ctrl+u/ctrl+d", "scroll half a page", "EVERYWHERE", nil},
	{"home/end", "top / bottom · chat: end returns to live", "EVERYWHERE", nil},
	{"ctrl+c", "quit", "EVERYWHERE", nil},

	{"enter", "send · approve a plan · empty input: open the last trace", "CHAT", nil},
	{"enter", "send", "", []int{ckViewChat}},
	{"shift+tab", "cycle the basic modes: ask · edit · plan · auto", "CHAT", nil},
	{"shift+tab", "mode", "", []int{ckViewChat}},
	{"m", "mode picker, advanced modes included (empty input)", "CHAT", nil},
	{"ctrl+o", "routing override for the next task — where it runs", "CHAT", nil},
	{"ctrl+o", "route", "", []int{ckViewChat}},
	{"esc", "cancel the running task · discard a plan · clear the input", "CHAT", nil},
	{"y/n", "approve / refuse a pending plan or file write", "CHAT", nil},
	{"d · x · o", "after an edit: diff · undo · open in $EDITOR (empty input)", "CHAT", nil},
	{"/ask /edit /plan /auto…", "set the mode by name (/architect /careful /unattended too)", "CHAT", nil},
	{":chat :agents :models :activity :usage :audit", "jump · :q quit", "CHAT", nil},

	{"j/k · ↑/↓", "move (the window follows the selection)", "LISTS", nil},
	{"j/k", "move", "", []int{ckViewAgents, ckViewModels, ckViewActivity, ckViewAudit}},
	{"g/G", "first / last row", "LISTS", nil},
	{"enter", "open / drill in", "LISTS", nil},
	{"enter", "trace", "", []int{ckViewAgents}},
	{"enter", "detail", "", []int{ckViewModels}},
	{"enter", "drill", "", []int{ckViewActivity}},
	{"enter", "resolve", "", []int{ckViewAudit}},
	{"space", "models: collapse provider", "LISTS", nil},
	{"space", "collapse", "", []int{ckViewModels}},
	{"r · p", "models: rescan · pin tier for chat", "LISTS", nil},
	{"p", "pin", "", []int{ckViewModels}},
	{"f · o · c · l", "activity: failures · open file · copy answer · audit log", "LISTS", nil},
	{"f", "failures", "", []int{ckViewActivity}},
	{"l", "audit log", "", []int{ckViewActivity}},
	{"m · t · d", "usage: by model · by tier · by day", "LISTS", nil},
	{"m/t/d", "group", "", []int{ckViewUsage}},
	{"v · i", "audit: verify chain · ignore item", "LISTS", nil},
	{"v", "verify", "", []int{ckViewAudit}},
	{"i", "ignore", "", []int{ckViewAudit}},
}

// ckBarBindings are the keys view v's status bar shows, in table order.
func ckBarBindings(v int) []ckBinding {
	var out []ckBinding
	for _, b := range ckKeymap {
		for _, bv := range b.views {
			if bv == v {
				out = append(out, b)
				break
			}
		}
	}
	return out
}

// key routes one keypress. Chat keeps every binding it always had — new shell
// keys apply only where they collide with nothing (#465's discipline).
func (m Cockpit) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyTab:
		return m.jump((m.view + 1) % ckViewCount()), nil
	case tea.KeyEsc:
		nm, cmd := m.escape()
		return nm, cmd
	case tea.KeyPgUp:
		return m.scrollBy(-m.pageSize()), nil
	case tea.KeyPgDown:
		return m.scrollBy(m.pageSize()), nil
	case tea.KeyCtrlU:
		return m.scrollBy(-m.pageSize() / 2), nil
	case tea.KeyCtrlD:
		return m.scrollBy(m.pageSize() / 2), nil
	case tea.KeyHome:
		return m.scrollBy(-ckScrollAll), nil
	case tea.KeyEnd:
		return m.scrollBy(ckScrollAll), nil
	}
	if m.glossary {
		// The overlay keeps scrolling (above) and closes on esc or ?; every
		// other key is swallowed. j/k scroll it too.
		if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 {
			switch msg.Runes[0] {
			case '?':
				m.glossary = false
			case 'j':
				return m.scrollBy(1), nil
			case 'k':
				return m.scrollBy(-1), nil
			}
		}
		return m, nil
	}
	if m.view == ckViewChat {
		return m.chatKey(msg)
	}
	return m.viewKey(msg)
}

// ckScrollAll is a jump "as far as it goes" — every offset is clamped against
// its real content length, so home/end reuse the ordinary scroll path.
const ckScrollAll = 1 << 20

// pageSize approximates one pane page from the terminal height.
func (m Cockpit) pageSize() int {
	p := m.h - 10
	if p < 3 {
		p = 3
	}
	return p
}

// scrollBy routes a scroll gesture (page keys, wheel) to whatever the active
// context scrolls: the glossary, the chat log, a table offset, or — in list
// views — the selection itself, whose window follows it.
func (m Cockpit) scrollBy(delta int) Cockpit {
	switch {
	case m.glossary:
		m.glossOff = ckClampOff(m.glossOff+delta, len(ckGlossaryLines()))
	case m.view == ckViewChat:
		m = m.chatScrollBy(delta)
	case m.view == ckViewUsage:
		m.usageOff = ckClampOff(m.usageOff+delta, len(m.usageRows()))
	case m.view == ckViewAudit:
		if m.audit != nil {
			m.scoreOff = ckClampOff(m.scoreOff+delta, len(m.audit.scorecard))
		}
	default:
		m = m.move(delta)
	}
	return m
}

// ckClampOff bounds a scroll offset by its content length; the renderer
// re-clamps exactly against the visible window.
func ckClampOff(off, n int) int {
	if off > n {
		off = n
	}
	if off < 0 {
		off = 0
	}
	return off
}

// jump switches to view v, closing transient overlays (glossary, picker,
// override) and running the on-entry hook. A pending plan/confirm survives —
// a question must not be lost by looking at activity.
func (m Cockpit) jump(v int) Cockpit {
	if !ckValidView(v) {
		return m
	}
	m.view = v
	m.glossary = false
	m.glossOff = 0
	m.modePick = false
	m.ovOpen, m.ovStage = false, 0
	m.flash = ""
	if v == ckViewAudit {
		m = m.loadAudit()
	}
	return m
}

// escape closes the topmost thing first: overlay, then modal stage, then a
// pending question, then typed input, then the running task itself.
func (m Cockpit) escape() (Cockpit, tea.Cmd) {
	switch {
	case m.glossary:
		m.glossary = false
		m.glossOff = 0
	case m.modePick:
		m.modePick = false
	case m.ovOpen:
		if m.ovStage != 0 {
			m.ovStage = 0 // back to the option list
		} else {
			m.ovOpen = false
		}
	case m.view == ckViewChat && m.confirm != nil:
		w := *m.confirm
		note := "stopped before writing — nothing changed"
		if w.phase == ckPhaseFix {
			note = "fix declined — the file keeps its last write"
		}
		return m.stopWait(w, note)
	case m.view == ckViewChat && m.planWait != nil:
		return m.stopWait(*m.planWait, "plan discarded — nothing ran")
	case m.view == ckViewChat && m.input != "":
		m.input = ""
	case m.view == ckViewChat && m.exec != nil:
		// Context cancellation through dispatch: the worker returns with a
		// cancelled result; exec clears when that message lands.
		m.exec.cancel()
		m.exec.setStage("cancelling…")
	case m.view == ckViewActivity && m.actDrill:
		m.actDrill = false
		m.traceOff = 0
	case m.view == ckViewModels && m.modelFocus:
		m.modelFocus = false
	}
	return m, nil
}

// viewKey handles keys on the non-chat views, where no text input exists so
// digits, '?', and letters are free to act as shortcuts.
func (m Cockpit) viewKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyUp:
		return m.move(-1), nil
	case tea.KeyDown:
		return m.move(1), nil
	case tea.KeyEnter:
		return m.enterRow()
	case tea.KeySpace:
		if m.view == ckViewModels {
			m = m.toggleCollapse()
		}
		return m, nil
	case tea.KeyRunes:
		if len(msg.Runes) != 1 {
			return m, nil
		}
		r := msg.Runes[0]
		if r >= '1' && r <= '6' {
			return m.jump(int(r - '1')), nil
		}
		switch r {
		case '?':
			m.glossary = true
			return m, nil
		case 'j':
			return m.move(1), nil
		case 'k':
			return m.move(-1), nil
		case 'g':
			return m.move(-ckScrollAll), nil
		case 'G':
			return m.move(ckScrollAll), nil
		}
		return m.viewRune(r)
	}
	return m, nil
}

// viewRune dispatches the per-view letter shortcuts.
func (m Cockpit) viewRune(r rune) (tea.Model, tea.Cmd) {
	switch m.view {
	case ckViewModels:
		switch r {
		case 'r':
			return m.startRescan()
		case 'p':
			m = m.pinSelected()
		}
	case ckViewActivity:
		switch r {
		case 'f':
			m.actFailOnly = !m.actFailOnly
			m.actSel, m.actDrill, m.traceOff = 0, false, 0
		case 'o':
			return m.openEditedFile()
		case 'c':
			// Run files record events, never model output — there is nothing
			// to copy until execution lands (#597). Say so instead of a no-op.
			m.flash = "no answer stored — run files keep events, not outputs"
		case 'l':
			return m.jump(ckViewAudit), nil
		}
	case ckViewUsage:
		switch r {
		case 'm', 't', 'd':
			m.usageGroup = byte(r)
			m.usageOff = 0
		}
	case ckViewAudit:
		switch r {
		case 'v':
			m = m.loadAudit()
			m.flash = "audit log re-verified"
		case 'i':
			m = m.ignoreAuditItem()
		}
	}
	return m, nil
}

// move shifts the active view's list selection by delta, clamped to the list.
func (m Cockpit) move(delta int) Cockpit {
	clamp := func(v, n int) int {
		if n == 0 || v < 0 {
			return 0
		}
		if v >= n {
			return n - 1
		}
		return v
	}
	switch m.view {
	case ckViewAgents:
		m.agentSel = clamp(m.agentSel+delta, len(m.agentRows()))
	case ckViewModels:
		m.modelSel = clamp(m.modelSel+delta, len(m.flatRows()))
	case ckViewActivity:
		if m.actDrill {
			// Bounded by a generous over-estimate of the trace's line count;
			// the renderer clamps exactly against the visible window.
			bound := 0
			if runs := m.activityRuns(); len(runs) > 0 {
				sel := m.actSel
				if sel < 0 || sel >= len(runs) {
					sel = 0
				}
				bound = len(runs[sel].events)*2 + 8
			}
			m.traceOff = ckClampOff(m.traceOff+delta, bound)
		} else {
			m.actSel = clamp(m.actSel+delta, len(m.activityRuns()))
		}
	case ckViewAudit:
		m.auditSel = clamp(m.auditSel+delta, len(m.auditItems()))
	}
	return m
}

// enterRow is enter on a list row: drill into a trace, focus a model's
// detail, or resolve/jump a needs-a-human item.
func (m Cockpit) enterRow() (tea.Model, tea.Cmd) {
	switch m.view {
	case ckViewAgents:
		rows := m.agentRows()
		if m.agentSel >= 0 && m.agentSel < len(rows) {
			m = m.focusRun(rows[m.agentSel].id)
		}
	case ckViewModels:
		if _, mr := m.selectedModel(); mr != nil {
			m.modelFocus = true
		}
	case ckViewActivity:
		if len(m.activityRuns()) > 0 {
			m.actDrill = true
			m.traceOff = 0
		}
	case ckViewAudit:
		m = m.resolveAuditItem()
	}
	return m, nil
}
