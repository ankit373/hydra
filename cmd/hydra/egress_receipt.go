// SPDX-License-Identifier: MIT

package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/util"
)

// egressReceipt is what one run sent where, read back from the ledger (#1885).
// It holds counts per kind of value, never a value or a hash of one.
type egressReceipt struct {
	RunID       string        `json:"run_id"`
	Heads       []headReceipt `json:"heads"`
	ChainIntact bool          `json:"chain_intact"`
}

type headReceipt struct {
	Head        string         `json:"head"`
	Provider    string         `json:"provider,omitempty"`
	Sink        string         `json:"sink"`
	Sensitivity string         `json:"sensitivity,omitempty"`
	Decision    string         `json:"decision"`
	Redacted    map[string]int `json:"redacted,omitempty"`
	Restored    int            `json:"restored,omitempty"`
}

// buildReceipt collects runID's exec events, one row per head asked, and adds
// the restore notes filed after each answer. Empty runID means the last run.
func buildReceipt(events []ledger.Event, runID string, chain ledger.ChainResult) (egressReceipt, error) {
	if runID == "" {
		for _, e := range slices.Backward(events) {
			if e.Provenance != nil && e.Provenance.RunID != "" && e.Action == ledger.Exec {
				runID = e.Provenance.RunID
				break
			}
		}
		if runID == "" {
			return egressReceipt{}, errors.New("the ledger holds no run with a recorded request yet")
		}
	}
	rc := egressReceipt{RunID: runID, ChainIntact: chain.Intact && !chain.Truncated}
	row := map[string]int{} // head -> index in rc.Heads
	for _, e := range events {
		p := e.Provenance
		if p == nil || p.RunID != runID {
			continue
		}
		if e.Action != ledger.Exec {
			if i, ok := row[e.Tool]; ok {
				rc.Heads[i].Restored += p.Restored
			}
			continue
		}
		row[e.Tool] = len(rc.Heads)
		rc.Heads = append(rc.Heads, headReceipt{
			Head: e.Tool, Provider: p.Provider, Sink: p.Sink, Sensitivity: p.Sensitivity,
			Decision: string(e.Decision), Redacted: p.Redacted,
		})
	}
	if len(rc.Heads) == 0 {
		return egressReceipt{}, fmt.Errorf("no request recorded for run %s", runID)
	}
	return rc, nil
}

func cmdEgress() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "egress",
		Short: "What left this machine, and to which provider",
	}
	cmd.AddCommand(cmdEgressReceipt())
	return cmd
}

func cmdEgressReceipt() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "receipt [run-id]",
		Short: "Per model asked in a run: provider, local or remote, and how many values were sent as placeholders",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			path := ledger.DefaultPath()
			events, err := ledger.Load(path)
			if err != nil {
				return err
			}
			runID := ""
			if len(args) == 1 {
				runID = args[0]
			}
			rc, err := buildReceipt(events, runID, ledger.VerifyChainEvents(events, path))
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(stdout).Encode(rc)
			}
			printReceipt(rc)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	return cmd
}

func printReceipt(rc egressReceipt) {
	fmt.Fprintf(stdout, "\n  %s %s\n", cortexStyle.Render("egress receipt"), dimStyle.Render("run "+util.SafeTerminal(rc.RunID)))
	fmt.Fprintln(stdout, dimStyle.Render("  "+strings.Repeat("─", 56)))
	for _, h := range rc.Heads {
		fmt.Fprintf(stdout, "  %s  %s  provider %s  %s  %s\n", util.SafeTerminal(h.Head), h.Sink,
			util.SafeTerminal(cmp.Or(h.Provider, "unknown")), cmp.Or(h.Sensitivity, "unclassified"), h.Decision)
		replaced := "none"
		if len(h.Redacted) > 0 {
			var parts []string
			for _, k := range slices.Sorted(maps.Keys(h.Redacted)) {
				parts = append(parts, fmt.Sprintf("%s %d", util.SafeTerminal(k), h.Redacted[k]))
			}
			replaced = strings.Join(parts, ", ")
		}
		fmt.Fprintf(stdout, "    %s\n", dimStyle.Render(fmt.Sprintf("sent as placeholders: %s   restored: %d", replaced, h.Restored)))
	}
	if rc.ChainIntact {
		fmt.Fprintf(stdout, "\n  ledger chain: verified\n\n")
	} else {
		fmt.Fprintf(stdout, "\n  %s ledger chain: BROKEN, this receipt may not match what happened\n\n", warnStyle.Render("⚠"))
	}
}
