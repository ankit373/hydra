// SPDX-License-Identifier: MIT

package main

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/ankit373/hydra/internal/idle"
	"github.com/ankit373/hydra/internal/payload"
)

func cmdContextPacks() *cobra.Command {
	return &cobra.Command{
		Use:   "packs [conversation]",
		Short: "List the packs staged for idle conversations: what each covers, its checks and its use",
		Long: "A pack is a conversation compacted while it was idle. Each line says which turns it covers, how many\n" +
			"probe questions agreed, its size before and after, and how often a request was sent it in place of\n" +
			"those turns. The conversation is the key `hyctl idle status` shows, or the name before the slash in a pack id.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return printPacks(idleDir(), args)
		},
	}
}

func printPacks(dir string, args []string) error {
	sums, err := idle.Summaries(dir)
	if err != nil {
		return err
	}
	if len(args) == 1 {
		key, ok := idle.Resolve(dir, args[0])
		if !ok {
			return fmt.Errorf("no conversation %q; `hyctl idle status` lists them", args[0])
		}
		sums = []idle.Summary{{Status: idle.Status{Key: key}}}
	}
	sort.Slice(sums, func(i, j int) bool { return sums[i].Key < sums[j].Key })
	shown := 0
	for _, s := range sums {
		packs, err := idle.Packs(dir, s.Key)
		if err != nil {
			return err
		}
		if len(packs) == 0 {
			continue
		}
		shown++
		note := ""
		if idle.Restored(dir, s.Key) {
			note = "  (restored: requests go out as they came)"
		}
		fmt.Fprintf(stdout, "%s%s\n", s.Key, note)
		for _, p := range packs {
			fmt.Fprintf(stdout, "  %s  turns %d-%d  probes %d/%d%s  %d to %d tokens  built %s on %s  %s\n",
				p.ID, p.Covers.From, p.Covers.To, p.Verdict.ProbeAgree, p.Verdict.Probes, partialNote(p.Verdict),
				p.Verdict.TokensBefore, p.Verdict.TokensAfter, p.Built.Format("2006-01-02 15:04"), p.Head, usedNote(p))
		}
	}
	if shown == 0 {
		fmt.Fprintln(stdout, "no staged packs; `hyctl idle status` says why nothing was compacted")
	}
	return nil
}

func partialNote(v idle.Verdict) string {
	if v.Partial {
		return " (partial: never swapped in automatically)"
	}
	return ""
}

func usedNote(p idle.PackInfo) string {
	if p.Used == 0 {
		return "not used yet"
	}
	return fmt.Sprintf("used %d times, last %s", p.Used, p.LastUsed.Format(time.DateTime))
}

func cmdContextRestore() *cobra.Command {
	return &cobra.Command{
		Use:   "restore <conversation>",
		Short: "Send every later request of a conversation as it came, and stop compacting it",
		Long: "Marks the conversation so no pack is sent in place of its turns again and none is built for it. The\n" +
			"originals were never removed. Their pins are dropped, except where another conversation's pack still\n" +
			"points to the same text.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			dir := idleDir()
			key, ok := idle.Resolve(dir, args[0])
			if !ok {
				return fmt.Errorf("no conversation %q; `hyctl idle status` lists them", args[0])
			}
			store, err := payload.Open(payload.Dir())
			if err != nil {
				return err
			}
			if err := idle.Restore(dir, key, store); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "restored %s: its next request goes out uncompacted\n", key)
			return nil
		},
	}
}

func cmdContextShowPack() *cobra.Command {
	return &cobra.Command{
		Use:   "show-pack <id>",
		Short: "Print what a pack sends to the model: the rules and the state, as written",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			st, err := idle.FindPack(idleDir(), args[0])
			if errors.Is(err, idle.ErrNoPack) {
				return fmt.Errorf("%w; `hyctl context packs` lists the ids", err)
			}
			if err != nil {
				return err
			}
			rules, state := st.Pack.Blocks()
			fmt.Fprintf(stdout, "%s%s\n%s%s", idle.RulesHeading, rules, idle.StateHeading, state)
			return nil
		},
	}
}
