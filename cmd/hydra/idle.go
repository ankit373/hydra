// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/ankit373/hydra/internal/budget"
	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/idle"
	"github.com/ankit373/hydra/internal/payload"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/serve"
	"github.com/ankit373/hydra/internal/sysinfo"
)

// idleMaxTokens bounds one answer from the local head: a note, or the state.
const idleMaxTokens = 2048

// idleHead asks one local head directly. Not a dispatch: nothing is routed,
// cached or sent anywhere but that head.
type idleHead struct {
	head provider.Head
	ask  func(ctx context.Context, system, text string) (string, error)
}

func (h idleHead) Head() provider.Head { return h.head }

func (h idleHead) Ask(ctx context.Context, prompt string) (string, error) {
	return h.ask(ctx, "", prompt)
}

func idleDir() string { return filepath.Join(config.Dir(), "idle") }

// newIdleScheduler wires the scheduler to this machine: the run log, the
// payload store, the dispatcher's heads, power and load.
func newIdleScheduler(d *dispatch.Dispatcher, cfg config.Idle) (*idle.Scheduler, error) {
	store, err := payload.Open(payload.Dir())
	if err != nil {
		return nil, err
	}
	heads := d.Heads()
	windows := budget.WindowsForHeads(config.ScriptHome(), heads)
	return idle.NewScheduler(idle.Env{
		Dir:        idleDir(),
		MaxJob:     time.Duration(cfg.MaxJobMinutes) * time.Minute,
		MaxNight:   time.Duration(cfg.MaxNightMinutes) * time.Minute,
		MinAgree:   cfg.MinAgree,
		Seen:       func() ([]idle.Seen, error) { return idle.RunLogSeen(time.Now()) },
		Activity:   idle.RunLogActivity,
		Heads:      func() []provider.Head { return heads },
		Windows:    func([]provider.Head) map[string]int { return windows },
		Machine:    idleMachine,
		Transcript: idle.Transcript,
		NewHead: func(h provider.Head) idle.Head {
			return idleHead{head: h, ask: dispatch.HeadAsk(h, idleMaxTokens)}
		},
		Store: store,
		Log: func(e runlog.Event) {
			_ = runlog.New(idle.RunPrefix + time.Now().UTC().Format("20060102")).Append(e)
		},
		ConvWindows: windows,
	}), nil
}

func idleMachine() idle.Machine {
	m := idle.Machine{CPUs: runtime.NumCPU()}
	m.OnAC, m.PowerKnown = sysinfo.OnACPower()
	m.Load, m.LoadKnown = sysinfo.LoadAverage()
	m.Pressure = sysinfo.Detect().MemPressure
	return m
}

// idleLoop ticks the scheduler once a minute until ctx ends.
func idleLoop(ctx context.Context, d *dispatch.Dispatcher, cfg config.Idle) {
	s, err := newIdleScheduler(d, cfg)
	if err != nil {
		fmt.Fprintln(stderr, "idle compaction not started:", err)
		return
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if _, err := s.Tick(ctx, now); err != nil {
				fmt.Fprintln(stderr, "idle tick:", err)
			}
		}
	}
}

func cmdIdle() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "idle",
		Short: "Compact idle conversations on a local model, and check the result before use",
		Long: "A conversation that has gone quiet is compacted on a local model into a pack: the goal, the\n" +
			"rules copied word for word, decisions with their reasons, and pointers back to every original.\n" +
			"A pack is only staged once it passes its checks. `hyctl serve` ticks this once a minute when\n" +
			"[idle] enabled = true in config.toml. It is off by default and never calls a paid model.",
	}
	cmd.AddCommand(cmdIdleRun(), cmdIdleStatus(), cmdIdleReplay())
	return cmd
}

func cmdIdleRun() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Run one tick by hand: compact the idle conversation with the most new text, if any",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			ctx := c.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			d, err := dispatch.New(ctx)
			if err != nil {
				return fmt.Errorf("dispatcher init: %w", err)
			}
			defer d.Close()
			s, err := newIdleScheduler(d, cfg.Idle)
			if err != nil {
				return err
			}
			res, err := s.Tick(ctx, time.Now())
			if err != nil {
				return err
			}
			switch res.Outcome {
			case idle.OutcomeStaged:
				fmt.Fprintf(stdout, "staged pack %d for %s\n", res.Pack, res.Conv)
			case idle.OutcomeRejected:
				fmt.Fprintf(stdout, "rejected pack %d for %s: %s\n", res.Pack, res.Conv, res.Reason)
			case idle.OutcomeCancelled:
				fmt.Fprintf(stdout, "stopped %s: %s\n", res.Conv, res.Reason)
			default:
				fmt.Fprintf(stdout, "nothing done: %s\n", res.Reason)
			}
			return nil
		},
	}
}

func cmdIdleStatus() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "List conversations, their last pack and the last reason one was refused",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return printIdleStatus(idleDir())
		},
	}
}

func printIdleStatus(dir string) error {
	sums, err := idle.Summaries(dir)
	if err != nil {
		return err
	}
	if len(sums) == 0 {
		fmt.Fprintln(stdout, "no conversation seen yet; run `hyctl idle run`, or turn on [idle] in config.toml")
		return nil
	}
	sort.Slice(sums, func(i, j int) bool { return sums[i].At.After(sums[j].At) })
	for _, s := range sums {
		pack := "no pack"
		if s.Pack > 0 && s.Verdict != nil {
			pack = fmt.Sprintf("pack %d, probes %d/%d, %d to %d tokens", s.Pack, s.Verdict.ProbeAgree, s.Verdict.Probes,
				s.Verdict.TokensBefore, s.Verdict.TokensAfter)
		}
		fmt.Fprintf(stdout, "%s  %s  %s\n", s.Key, s.Outcome, pack)
		if s.Reason != "" {
			fmt.Fprintf(stdout, "    last reason: %s\n", s.Reason)
		}
		if s.Rejected > 0 {
			fmt.Fprintf(stdout, "    %d rejected pack(s) kept in %s\n", s.Rejected, filepath.Join(dir, s.Key))
		}
	}
	return nil
}

// logCompacted records on the request's own run that a pack was sent, so
// `hyctl trace` shows it beside the answer.
func logCompacted(run, task string, c *serve.Compacted) {
	_ = runlog.New(run).Append(runlog.Event{
		TaskID: task, Kind: runlog.KindIdleUsed, Detail: c.Pack,
		Meta: map[string]any{"pack": c.Pack, "probes": c.Probes, "probe_agree": c.ProbeAgree,
			"tokens_before": c.TokensBefore, "tokens_after": c.TokensAfter},
	})
}

func cmdIdleReplay() *cobra.Command {
	var headID string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "replay <dir>",
		Short: "Does a checked pack keep what later turns need? Compare it with masking and with no compaction",
		Long: "Reads recorded conversations (*.json: name, kind, license, turns, and needles, the facts and rules\n" +
			"asked for later), compacts each five times, and scores four arms after rounds 1, 3 and 5: nothing,\n" +
			"masking (old assistant and tool output cut to one line), a pack that must pass its checks, and the\n" +
			"same pack unchecked. Per kind of chat it prints tokens, probe agreement and needle recall, and says\n" +
			"whether the checked pack beat masking. One local model does everything; nothing is sent anywhere else.",
		Example: "  hyctl idle replay bench/idle-replay --head ollama/qwen3:8b",
		Args:    cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			fixtures, err := idle.LoadFixtures(args[0])
			if err != nil {
				return err
			}
			d, err := dispatch.New(ctx)
			if err != nil {
				return fmt.Errorf("dispatcher init: %w", err)
			}
			defer d.Close()
			head, err := pickHead(d.Heads(), headID)
			if err != nil {
				return err
			}
			if !head.LocalOnly {
				return fmt.Errorf("%s is not a local model; a replay never calls a paid one", head.ID)
			}
			fmt.Fprintf(stdout, "model %s (%s), digest %s; local only, no paid calls\n\n", head.ID, head.Name, orUnknown(head.Meta["model_digest"]))
			rows, err := idle.Replay(ctx, idleHead{head: head, ask: dispatch.HeadAsk(head, idleMaxTokens)}, fixtures, idle.ReplayConfig{})
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(stdout).Encode(map[string]any{"head": head.ID, "digest": head.Meta["model_digest"], "rows": rows})
			}
			fmt.Fprint(stdout, idle.Tables(rows))
			return nil
		},
	}
	cmd.Flags().StringVar(&headID, "head", "", "the local model to use (default: the first routable local one)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "one JSON object with every row")
	return cmd
}

func orUnknown(s string) string {
	if s == "" {
		return "not reported"
	}
	return s
}
