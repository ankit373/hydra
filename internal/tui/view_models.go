// SPDX-License-Identifier: MIT

package tui

// view_models.go — view 2: every scanned model, nested under its
// provider/server, with a detail panel for the selected one. Replaces the old
// dashboard's flat fleet table, where the Ollama binary and its models were
// siblings that could contradict each other (server down · model up).

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/pricing"
	"github.com/ankit373/hydra/internal/probe"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
)

type ckModelRow struct {
	id       string
	name     string
	provider string
	tier     int
	capScore int
	up       bool
	reason   string // unroutable reason, "" when up
	embed    bool   // embeddings only — never routed
	local    bool
}

type ckModelGroup struct {
	name string
	down bool   // server binary present but nothing routable behind it
	note string // why (the binary head's unroutable reason)
	rows []ckModelRow
}

// ckServerFor names the group a head belongs to and whether the head is a
// binary-on-PATH pseudo-head — the group's header, never a sibling row.
func ckServerFor(h provider.Head) (group string, isBinary bool) {
	switch {
	case h.ID == "ollama":
		return "Ollama", true
	case h.ID == "llamafile":
		return "Llamafile", true
	case strings.HasPrefix(h.ID, "ollama/"):
		return "Ollama", false
	case strings.HasPrefix(h.ID, "lmstudio/"):
		return "LM Studio", false
	}
	return h.Provider, false
}

// ckGroupHeads folds the scan result into provider/server groups, in scan
// rank order so the tree mirrors the order routing would consider.
func ckGroupHeads(heads []provider.Head) []ckModelGroup {
	index := map[string]int{}
	var groups []ckModelGroup
	at := func(name string) int {
		if i, ok := index[name]; ok {
			return i
		}
		index[name] = len(groups)
		groups = append(groups, ckModelGroup{name: name})
		return len(groups) - 1
	}
	for _, h := range heads {
		name, isBinary := ckServerFor(h)
		i := at(name)
		if isBinary {
			// The binary alone is not routable; keep its reason as the group's
			// server-down note, used only if no routable child shows up.
			groups[i].note = executor.Unroutable(h)
			continue
		}
		reason := executor.Unroutable(h)
		groups[i].rows = append(groups[i].rows, ckModelRow{
			id:       h.ID,
			name:     ckStripGroupSuffix(h.Name, name),
			provider: h.Provider,
			tier:     rank.UITier(h),
			capScore: h.CapScore,
			up:       reason == "",
			reason:   reason,
			embed:    h.Meta["embedding_only"] == "true",
			local:    h.LocalOnly,
		})
	}
	for i := range groups {
		groups[i].down = len(groups[i].rows) == 0
	}
	return groups
}

// ckStripGroupSuffix drops a " (Ollama)"-style suffix that just repeats the
// group header the row already sits under.
func ckStripGroupSuffix(name, group string) string {
	return strings.TrimSuffix(name, " ("+group+")")
}

// ── selection ─────────────────────────────────────────────────────────────────

// ckFlatRow addresses one visible tree row: r == -1 is the group header.
type ckFlatRow struct{ g, r int }

// flatRows is the tree flattened for j/k selection, honouring collapse.
func (m Cockpit) flatRows() []ckFlatRow {
	var out []ckFlatRow
	for g := range m.groups {
		out = append(out, ckFlatRow{g, -1})
		if m.collapsed[m.groups[g].name] {
			continue
		}
		for r := range m.groups[g].rows {
			out = append(out, ckFlatRow{g, r})
		}
	}
	return out
}

// selectedModel returns the selected group and, when a model row is selected,
// the model. Never panics on a stale selection.
func (m Cockpit) selectedModel() (*ckModelGroup, *ckModelRow) {
	flat := m.flatRows()
	if len(flat) == 0 {
		return nil, nil
	}
	sel := m.modelSel
	if sel < 0 || sel >= len(flat) {
		sel = 0
	}
	f := flat[sel]
	g := &m.groups[f.g]
	if f.r < 0 {
		return g, nil
	}
	return g, &g.rows[f.r]
}

// toggleCollapse folds/unfolds the selected row's provider group.
func (m Cockpit) toggleCollapse() Cockpit {
	if g, _ := m.selectedModel(); g != nil {
		m.collapsed[g.name] = !m.collapsed[g.name]
	}
	return m
}

// pinSelected makes the selected model's tier the session default for chat.
// Pinning the already-pinned tier unpins.
func (m Cockpit) pinSelected() Cockpit {
	_, mr := m.selectedModel()
	if mr == nil {
		m.flash = "select a model to pin its tier"
		return m
	}
	if m.pinnedTier == mr.tier {
		m.pinnedTier = 0
		m.flash = "tier unpinned"
		return m
	}
	m.pinnedTier = mr.tier
	m.flash = fmt.Sprintf("chat pinned to T%d", mr.tier)
	return m
}

// ── rescan ────────────────────────────────────────────────────────────────────

type ckRescanMsg struct {
	heads []provider.Head
	at    time.Time
}

func probeContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), ckProbeTimeout)
}

// startRescan kicks off an async machine scan; the UI stays responsive and
// the footer says "scanning…" until the result lands.
func (m Cockpit) startRescan() (tea.Model, tea.Cmd) {
	if m.scanning {
		return m, nil
	}
	m.scanning = true
	return m, func() tea.Msg {
		ctx, cancel := probeContext()
		defer cancel()
		return ckRescanMsg{heads: probe.Run(ctx).Heads, at: time.Now()}
	}
}

// applyRescan swaps in the fresh scan everywhere the old one was used.
func (m Cockpit) applyRescan(msg ckRescanMsg) Cockpit {
	m.scanning = false
	m.probedHeads = msg.heads
	m.heads = ckHeadsFrom(msg.heads, pricing.Load())
	m.groups = ckGroupHeads(msg.heads)
	m.scannedAt = msg.at
	if flat := m.flatRows(); m.modelSel >= len(flat) {
		m.modelSel = 0
	}
	return m
}

// ── render ────────────────────────────────────────────────────────────────────

// ckModelNameW is the tree's name column budget — fixed so a long name in one
// row never shifts its siblings' columns.
const ckModelNameW = 22

func (m Cockpit) viewModels(w, h int) string {
	tree := ckBoxS.Render(m.modelTree(h - 2))
	detail := m.modelDetail(w)
	return lipgloss.NewStyle().Width(w).Height(h).Render(ckSplit(w, tree, detail, m.modelFocus))
}

func (m Cockpit) modelTree(h int) string {
	var b strings.Builder
	b.WriteString(ckLabelS.Render("MODELS · by provider/server") + "\n\n")

	flat := m.flatRows()
	if len(flat) == 0 {
		b.WriteString(ckFaintS.Render(" nothing scanned — run `hyctl probe` to see why") + "\n")
	}
	sel := m.modelSel
	if sel < 0 || sel >= len(flat) {
		sel = 0
	}

	// Distinct truncation is computed per sibling set, so shared prefixes are
	// dropped instead of producing identical truncated rows.
	names := make(map[int][]string, len(m.groups))
	for g := range m.groups {
		raw := make([]string, len(m.groups[g].rows))
		for r, row := range m.groups[g].rows {
			raw[r] = ckSafe(row.name)
		}
		names[g] = ckDistinctTruncate(raw, ckModelNameW)
	}

	rows := make([]string, len(flat))
	for i, f := range flat {
		marker := "  "
		if i == sel {
			marker = ckAquaS.Render("▸ ")
		}
		g := m.groups[f.g]
		if f.r < 0 {
			fold := "▾"
			if m.collapsed[g.name] {
				fold = "▸"
			}
			glyph, meta := ckCheapS.Render("●"), fmt.Sprintf("%d model%s", len(g.rows), plural(len(g.rows)))
			if g.down {
				glyph, meta = ckExpS.Render("○"), "server down · "+truncate(g.note, 34)
			} else if g.upCount() == 0 {
				glyph = ckExpS.Render("○")
			}
			rows[i] = marker + ckFaintS.Render(fold) + " " + glyph + " " +
				ckInkS.Bold(i == sel).Render(ckCell(ckSafe(g.name), ckModelNameW)) +
				"     " + ckDimS.Render(meta)
			continue
		}
		row := g.rows[f.r]
		glyph, style, meta := ckCheapS.Render("●"), ckVioletS, ""
		switch {
		case row.embed:
			glyph, style, meta = ckFaintS.Render("◌"), ckFaintS, "embeddings only — never routed"
		case !row.up:
			glyph, style, meta = ckFaintS.Render("◌"), ckFaintS, "not routable"
		}
		rows[i] = marker + "   " + glyph + " " +
			style.Bold(i == sel).Render(ckCell(names[f.g][f.r], ckModelNameW)) + " " +
			ckDimS.Render(ckRCell(fmt.Sprintf("T%d", row.tier), 3)) + " " +
			ckFaintS.Render(meta)
	}
	// Window the rows so the selection stays visible at any terminal height.
	avail := h - 4 // title + blank above, blank + legend below
	if avail < 3 {
		avail = 3
	}
	b.WriteString(strings.Join(ckSelScroll(rows, sel, avail), "\n"))
	b.WriteString("\n\n" + ckFaintS.Render("● up · ○ down (children unavailable) · ◌ not routable"))
	return b.String()
}

func (g ckModelGroup) upCount() int {
	n := 0
	for _, r := range g.rows {
		if r.up {
			n++
		}
	}
	return n
}

// modelDetail is the right panel: everything measured about the selection.
// A figure with no data renders "—", never a placeholder value.
func (m Cockpit) modelDetail(w int) string {
	g, mr := m.selectedModel()
	box := ckBoxS
	if m.modelFocus {
		box = box.BorderForeground(ckAqua)
	}
	if g == nil {
		return box.Render(ckLabelS.Render("MODEL") + "\n\n " + ckFaintS.Render("nothing selected"))
	}
	kv := func(k, v string) string { return " " + ckDimS.Render(ckCell(k, 12)) + ckInkS.Render(truncate(v, 26)) }

	if mr == nil {
		lines := []string{
			ckLabelS.Render("SERVER"), "",
			" " + ckInkS.Bold(true).Render(truncate(ckSafe(g.name), 38)), "",
			kv("models", fmt.Sprintf("%d (%d routable)", len(g.rows), g.upCount())),
		}
		if g.down {
			lines = append(lines, kv("state", "down"), " "+ckFaintS.Render(truncate(g.note, 38)))
		} else {
			lines = append(lines, kv("state", "up"))
		}
		if m.modelFocus {
			lines = append(lines, "", " "+ckFaintS.Render("esc back to the list"))
		}
		return box.Render(strings.Join(lines, "\n"))
	}

	st := m.metrics.ckStatFor(mr.name, mr.id)
	p50 := "—"
	if v := st.p50(); v > 0 {
		p50 = ckFmtMS(v)
	}
	lastUsed := "—"
	if st.lastRunID != "" {
		lastUsed = "trace " + truncate(st.lastRunID, 20)
	} else if st.lastTS != "" {
		lastUsed = truncate(st.lastTS, 19)
	}
	lines := []string{
		ckLabelS.Render("MODEL"), "",
		" " + lipgloss.NewStyle().Foreground(ckTierColor(mr.tier)).Bold(true).Render(truncate(ckSafe(mr.name), 38)), "",
		kv("provider", mr.provider),
		kv("tier", fmt.Sprintf("T%d", mr.tier)),
		kv("capability", fmt.Sprintf("%d", mr.capScore)),
		kv("p50 latency", p50),
		kv("last used", lastUsed),
		kv("requests", fmt.Sprintf("%d today", st.reqsToday)),
		kv("cost", fmt.Sprintf("$%.4f today", st.costToday)),
	}
	switch {
	case mr.embed:
		lines = append(lines, "", " "+ckFaintS.Render("embeddings only — never routed"))
	case !mr.up:
		lines = append(lines, "", " "+ckExpS.Render("not routable"), " "+ckFaintS.Render(truncate(mr.reason, 38)))
	}
	if m.modelFocus {
		lines = append(lines, kv("id", mr.id), " "+ckFaintS.Render("esc back to the list"))
	}

	lines = append(lines, "", ckLabelS.Render("SCORECARD")+ckDimS.Render(" · calibration"))
	stats := m.metrics.ckScorecardFor(mr.id, mr.name)
	if len(stats) == 0 {
		lines = append(lines, " "+ckFaintS.Render("no scored verdicts yet for this model"))
	}
	for i, s := range stats {
		if i >= 3 && !m.modelFocus {
			lines = append(lines, " "+ckFaintS.Render(fmt.Sprintf("… %d more (enter for all)", len(stats)-i)))
			break
		}
		lines = append(lines, " "+ckDimS.Render(ckCell(s.Domain, 9))+
			ckCyanS.Render(fmt.Sprintf("n %.0f · sens %.2f · spec %.2f · D %.2f", s.N, s.Se, s.Sp, s.D)))
	}
	return box.Render(strings.Join(lines, "\n"))
}

// p50 is the median of the recorded wall times; 0 when the model never ran.
func (s ckModelStat) p50() int64 {
	if len(s.wall) == 0 {
		return 0
	}
	sorted := append([]int64(nil), s.wall...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}
