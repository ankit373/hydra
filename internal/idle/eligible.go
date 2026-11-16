// SPDX-License-Identifier: MIT

package idle

import (
	"fmt"
	"time"

	"github.com/ankit373/hydra/internal/budget"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/sysinfo"
)

// Config holds the eligibility thresholds. Zero fields take DefaultConfig's.
type Config struct {
	IdleAfter     time.Duration
	MinFillPct    int
	MinRisk       float64
	MinSpanTokens int
	MaxLoadPerCPU float64
}

// DefaultConfig: two quiet minutes, the compact band's 50%, the governor's
// warning risk, and a span big enough to be worth a local model's time.
var DefaultConfig = Config{IdleAfter: 2 * time.Minute, MinFillPct: 50, MinRisk: 0.5,
	MinSpanTokens: 8000, MaxLoadPerCPU: 0.5}

func (c Config) orDefault() Config {
	d := DefaultConfig
	if c.IdleAfter > 0 {
		d.IdleAfter = c.IdleAfter
	}
	if c.MinFillPct > 0 {
		d.MinFillPct = c.MinFillPct
	}
	if c.MinRisk > 0 {
		d.MinRisk = c.MinRisk
	}
	if c.MinSpanTokens > 0 {
		d.MinSpanTokens = c.MinSpanTokens
	}
	if c.MaxLoadPerCPU > 0 {
		d.MaxLoadPerCPU = c.MaxLoadPerCPU
	}
	return d
}

// Machine is what the scheduler read about this computer just now.
type Machine struct {
	OnAC, PowerKnown bool
	Load             float64
	LoadKnown        bool
	CPUs             int
	Pressure         sysinfo.Pressure
}

// Conversation is one conversation's state as the scheduler observed it.
type Conversation struct {
	Key       string
	LastEvent time.Time    // newest event in any of its runs
	Quota     budget.Quota // the window it fills
	NewTokens int          // tokens of turns no pack covers yet
}

// Job is a conversation to compact and the local head to do it on.
type Job struct {
	Conv   string
	Head   provider.Head
	Window int
	Tokens int
}

// Eligible decides whether conv may be compacted now. Every refusal says why,
// so `hyctl idle status` can explain why nothing ran. windows is
// budget.WindowsForHeads over heads.
func Eligible(conv Conversation, heads []provider.Head, windows map[string]int, m Machine,
	now time.Time, cfg Config) (Job, bool, string) {
	cfg = cfg.orDefault()
	if quiet := now.Sub(conv.LastEvent); quiet < cfg.IdleAfter {
		return Job{}, false, fmt.Sprintf("active %s ago; idle after %s", quiet.Round(time.Second), cfg.IdleAfter)
	}
	_, risk := conv.Quota.Burn()
	if conv.Quota.Pct() < cfg.MinFillPct && risk <= cfg.MinRisk {
		return Job{}, false, fmt.Sprintf("window %d%% full, under %d%%, and burn risk %.2f is low",
			conv.Quota.Pct(), cfg.MinFillPct, risk)
	}
	if conv.NewTokens < cfg.MinSpanTokens {
		return Job{}, false, fmt.Sprintf("%d new tokens, under the %d worth compacting", conv.NewTokens, cfg.MinSpanTokens)
	}
	job, ok := pickHead(heads, windows, cfg.MinSpanTokens)
	if !ok {
		return Job{}, false, fmt.Sprintf("no local model that is up has a window of %d tokens", cfg.MinSpanTokens)
	}
	if !m.PowerKnown {
		return Job{}, false, "power source unknown"
	}
	if !m.OnAC {
		return Job{}, false, "on battery"
	}
	if !m.LoadKnown || m.CPUs <= 0 {
		return Job{}, false, "load unknown"
	}
	if per := m.Load / float64(m.CPUs); per > cfg.MaxLoadPerCPU {
		return Job{}, false, fmt.Sprintf("load %.2f per CPU, over %.2f", per, cfg.MaxLoadPerCPU)
	}
	if m.Pressure != sysinfo.PressureLow {
		return Job{}, false, fmt.Sprintf("memory pressure %s", m.Pressure)
	}
	job.Conv, job.Tokens = conv.Key, conv.NewTokens
	return job, true, ""
}

// pickHead is the ready local head with the largest window; the span is cut
// into groups that fit it, so it needs room for one group.
func pickHead(heads []provider.Head, windows map[string]int, group int) (Job, bool) {
	var best Job
	for _, h := range heads {
		if w := windows[h.ID]; h.LocalOnly && h.AuthReady && w >= group && w > best.Window {
			best = Job{Head: h, Window: w}
		}
	}
	return best, best.Window > 0
}
