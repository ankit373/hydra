// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-isatty"

	"github.com/ankit373/hydra/internal/swarm"
)

// ensemblePanel draws a fan-out while it runs: which heads are working, how
// long each has taken, and on an SPRT run how far the evidence has moved Λ.
//
// Token streaming does not apply here. With N heads answering at once there is
// no single stream to show, and in `best` mode the answer is not known until a
// judge has read all of them, so the two slowest things hyctl does were the two
// that showed the least (#798).
//
// Only used when stdout is a terminal. A pipe gets nothing, so anything that
// parses hyctl's output is unaffected.
type ensemblePanel struct {
	w     io.Writer
	width int
	title string

	mu    sync.Mutex
	rows  []*panelRow
	byID  map[string]*panelRow
	drawn int // lines on screen, so a repaint moves back over exactly them
	frame int
	spent float64

	// lambda/threshold are the SPRT footer: where the evidence stands against
	// the log-odds it must cross. haveLLR keeps a run nothing has been weighed
	// in yet from rendering a 0.00 that reads as measured.
	lambda     float64
	threshold  float64
	confidence float64
	haveLLR    bool

	stop chan struct{}
	done chan struct{}
}

// panelRow is one head's line. Separate from swarm.Attempt because a row
// exists before the attempt does: a queued head has a name and nothing else.
type panelRow struct {
	name    string
	state   rowState
	started time.Time
	dur     time.Duration
	tokens  int
	status  swarm.HeadStatus
	note    string // "agrees"/"disagrees", once the ensemble has weighed it
}

type rowState int

const (
	rowQueued rowState = iota
	rowRunning
	rowDone
)

// newEnsemblePanelIf builds a panel only for a run that should draw one: a
// live terminal, and not --no-stream. Everything below is nil-safe, so a piped
// run carries a nil panel rather than a branch at every call site.
func newEnsemblePanelIf(want bool, title string) *ensemblePanel {
	if !want || !isatty.IsTerminal(os.Stdout.Fd()) {
		return nil
	}
	w, _ := terminalSize()
	p := newEnsemblePanel(os.Stdout, w, title)
	p.animate()
	return p
}

// handler is the callback to hand swarm, and nil when there is no panel. A
// method value on a nil receiver is not itself nil, so returning p.Handle
// unconditionally would make every run pay for the progress path.
func (p *ensemblePanel) handler() func(swarm.Progress) {
	if p == nil {
		return nil
	}
	return p.Handle
}

func newEnsemblePanel(w io.Writer, width int, title string) *ensemblePanel {
	return &ensemblePanel{w: w, width: width, title: title, byID: map[string]*panelRow{}}
}

// animate repaints on a timer until Stop. Separate from the constructor
// because everything the panel reports on is a blocking wait: with no tick the
// elapsed column would freeze at whatever the last event left it, which is the
// appearance of nothing happening this exists to remove.
func (p *ensemblePanel) animate() {
	p.stop, p.done = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(p.done)
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-p.stop:
				return
			case <-t.C:
				p.mu.Lock()
				p.frame++
				p.paintLocked()
				p.mu.Unlock()
			}
		}
	}()
}

// Handle turns one swarm event into a repaint. Safe to pass as
// swarm.Options.OnProgress: swarm serializes delivery, and the ticker takes
// this same lock.
func (p *ensemblePanel) Handle(ev swarm.Progress) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	switch ev.Kind {
	case swarm.ProgressSelected:
		for _, h := range ev.Heads {
			r := &panelRow{name: h.Name, state: rowQueued}
			p.rows = append(p.rows, r)
			p.byID[h.ID] = r
		}
	case swarm.ProgressStarted:
		if r := p.byID[ev.Head.ID]; r != nil {
			r.state, r.started = rowRunning, time.Now()
		}
	case swarm.ProgressFinished:
		if r := p.byID[ev.Head.ID]; r != nil {
			r.state, r.status = rowDone, ev.Attempt.Status
			r.dur, r.tokens = ev.Attempt.Duration, ev.Attempt.TotalTokens()
		}
		p.spent += ev.Attempt.EstCostUSD
	case swarm.ProgressEvidence:
		p.lambda, p.threshold = ev.Evidence.LambdaAfter, ev.Threshold
		p.confidence, p.haveLLR = ev.Evidence.ConfidenceAfter, true
		if r := p.byID[ev.Evidence.Source]; r != nil {
			r.note = "agrees"
			if !ev.Evidence.Agreed {
				r.note = "disagrees"
			}
		}
	}
	p.paintLocked()
}

// Stop ends the animation and erases the panel. The result block printed after
// it reports the same run in full, so leaving the panel up would show every
// head twice.
func (p *ensemblePanel) Stop() {
	if p == nil {
		return
	}
	if p.done != nil {
		close(p.stop)
		<-p.done
		p.done = nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.eraseLocked()
}

func (p *ensemblePanel) eraseLocked() {
	if p.drawn == 0 {
		return
	}
	fmt.Fprintf(p.w, "\r\033[%dA\033[J", p.drawn)
	p.drawn = 0
}

var panelSpinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// paintLocked redraws the whole panel in place. Every line is cut to the
// terminal width before anything is printed, so the number of lines drawn is
// exactly the number to move back over, which is what makes the erase safe.
func (p *ensemblePanel) paintLocked() {
	if len(p.rows) == 0 {
		return
	}
	p.eraseLocked()

	rule := dimStyle.Render("  " + strings.Repeat("─", p.ruleWidth()))
	lines := []string{"  " + cortexStyle.Render(cut(p.title, p.width-3)), rule}
	for _, r := range p.rows {
		lines = append(lines, p.rowLine(r))
	}
	lines = append(lines, rule)
	if foot := p.footer(); foot != "" {
		lines = append(lines, foot)
	}

	fmt.Fprintln(p.w, "\n"+strings.Join(lines, "\n"))
	p.drawn = len(lines) + 1 // the blank line this opens with
}

// ruleWidth never exceeds the terminal: a floor that outran a narrow one would
// wrap the rule, and a wrapped line is what makes the erase count wrong.
func (p *ensemblePanel) ruleWidth() int {
	return clamp(p.width-4, 1, 56)
}

// rowLine builds the row as plain text, cuts it to the terminal, and only then
// colours it. Styling first would make the cut count escape sequences as
// printed width and wrap the row, and a wrapped row makes the erase count
// wrong for every repaint after it.
func (p *ensemblePanel) rowLine(r *panelRow) string {
	mark, style, tail := "·", dimStyle, "queued"
	switch r.state {
	case rowRunning:
		mark, tail = panelSpinner[p.frame%len(panelSpinner)], elapsed(time.Since(r.started))
	case rowDone:
		mark, style = "✓", okStyle
		if r.status != swarm.StatusOK {
			mark, style = "✗", warnStyle
		}
		tail = fmt.Sprintf("%-7s %5d tok  %s", elapsed(r.dur), r.tokens, finishedNote(r))
	}

	row := strings.TrimRight("  "+mark+" "+pad(r.name, p.nameWidth())+"  "+tail, " ")
	plain := []rune(cut(row, p.width-1))
	if len(plain) < 4 {
		return string(plain)
	}
	return "  " + style.Render(string(plain[2])) + dimStyle.Render(string(plain[3:]))
}

// nameWidth leaves room for the mark and the finished columns, and never lets
// the name squeeze them out on a narrow terminal. 28 matches the head column
// the result table prints afterwards, so a name is not cut in one and whole in
// the other.
func (p *ensemblePanel) nameWidth() int {
	return clamp(p.width-32, 8, 28)
}

// finishedNote says what the run made of the head: the SPRT verdict where
// there is one, otherwise how it ended. A plain success says nothing, the tick
// already did.
func finishedNote(r *panelRow) string {
	if r.note != "" {
		return r.note
	}
	if r.status == swarm.StatusOK {
		return ""
	}
	return string(r.status)
}

// footer is the SPRT line, and the reason this beats a spinner: the run is
// walking Λ toward a threshold it already knows, so it can say why it has not
// stopped yet. A swarm fan-out has no threshold, and gets spend alone.
func (p *ensemblePanel) footer() string {
	spend := fmt.Sprintf("$%.4f so far", p.spent)
	if !p.haveLLR {
		if p.spent == 0 {
			return ""
		}
		return "  " + dimStyle.Render(cut(spend, p.width-3))
	}
	return "  " + dimStyle.Render(cut(fmt.Sprintf("Λ %+.2f / need %+.2f  ·  %.1f%%  ·  %s",
		p.lambda, p.threshold, p.confidence*100, spend), p.width-3))
}

// elapsed renders a duration at the resolution someone waiting actually reads.
func elapsed(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func pad(s string, n int) string {
	if utf8.RuneCountInString(s) > n {
		return string([]rune(s)[:n-1]) + "…"
	}
	return s + strings.Repeat(" ", n-utf8.RuneCountInString(s))
}

func cut(s string, n int) string {
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
