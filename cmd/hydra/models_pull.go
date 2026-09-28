// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/ankit373/hydra/internal/modelpull"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/sysinfo"
)

// usableBytesForPull is how much memory a pulled model can have, or 0 when the
// hardware could not be read.
//
// Zero disables the size guard rather than refusing everything: the absence of
// a reading is not a verdict about the machine, the same call #258 made when
// hardware-unknown was ranking every local model as "insufficient memory".
func usableBytesForPull(s *sysinfo.Specs) int64 {
	if !s.HardwareKnown() {
		return 0
	}
	return int64(s.EffectiveVRAMGB() * 1e9)
}

// cmdModelsPull fetches a model into the local Ollama server.
func cmdModelsPull() *cobra.Command {
	var force, jsonOut bool
	cmd := &cobra.Command{
		Use:   "pull <ref>",
		Short: "Download a model into the local Ollama server (Ollama or HuggingFace GGUF ref)",
		Long: "Fetch a model so this machine can route to it.\n\n" +
			"<ref> is whatever the local server resolves: an Ollama library name\n" +
			"(qwen3:8b) or a HuggingFace GGUF repo (hf.co/<user>/<repo>).\n\n" +
			"Nothing else in hyctl downloads weights. This is the only command that\n" +
			"reaches the network for them, and only when you run it.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := args[0]
			host := provider.OllamaHost()

			// Taken before the pull so the head it produced can be named
			// exactly, rather than derived from the ref: Ollama rewrites a
			// HuggingFace ref into a name of its own.
			before, err := modelpull.Installed(cmd.Context(), host)
			if err != nil {
				return pullServerError(err, host)
			}

			usable := int64(0)
			if !force {
				usable = usableBytesForPull(sysinfo.Detect())
			}

			progress := newPullProgress(jsonOut)
			err = modelpull.Pull(cmd.Context(), host, ref, modelpull.Options{
				UsableBytes: usable,
				OnProgress:  progress.handle,
			})
			progress.done()
			if err != nil {
				return pullError(err, force)
			}

			after, _ := modelpull.Installed(cmd.Context(), host)
			return reportPulled(ref, modelpull.Added(before, after), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "pull even when the model looks too large for this machine's memory")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit progress and the result as JSON")
	return cmd
}

// pullServerError turns an unreachable server into the one actionable line a
// user needs, rather than a transport error they have to interpret (#248).
func pullServerError(err error, host string) error {
	if errors.Is(err, modelpull.ErrServerDown) {
		return fmt.Errorf("no Ollama server at %s. Start one with `ollama serve`, "+
			"or point $OLLAMA_HOST at the machine running it", host)
	}
	return err
}

// pullError explains a refusal in terms of what to do next.
func pullError(err error, forced bool) error {
	if errors.Is(err, modelpull.ErrTooLarge) && !forced {
		return fmt.Errorf("%w.\n  Pick a smaller quantization (Q4_K_M rather than Q8_0 or F16), "+
			"or pass --force if this machine has more headroom than it reports", err)
	}
	return err
}

// reportPulled names the head the router will use, read back from the server
// rather than derived from the ref.
func reportPulled(ref string, added []string, jsonOut bool) error {
	heads := make([]string, len(added))
	for i, name := range added {
		heads[i] = "ollama/" + name
	}
	if jsonOut {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"ref": ref, "pulled": true, "heads": heads,
		})
	}
	switch len(heads) {
	case 0:
		// Already present: a re-pull is not a failure, and claiming a new head
		// appeared would be a lie about what changed.
		fmt.Printf("  %s %s was already installed; nothing new to route to.\n", okStyle.Render("✓"), ref)
	default:
		fmt.Printf("  %s pulled %s\n", okStyle.Render("✓"), ref)
		for _, h := range heads {
			fmt.Printf("    routable as %s\n", cortexStyle.Render(h))
		}
	}
	return nil
}

// pullProgress renders a pull in flight. One line, rewritten in place on a
// terminal; one line per status change when piped, so a log stays readable
// and byte-stable.
type pullProgress struct {
	jsonOut  bool
	tty      bool
	lastStat string
	wrote    bool
}

func newPullProgress(jsonOut bool) *pullProgress {
	return &pullProgress{jsonOut: jsonOut, tty: isatty.IsTerminal(os.Stdout.Fd())}
}

func (p *pullProgress) handle(pr modelpull.Progress) {
	if p.jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(pr)
		return
	}
	line := pullLine(pr)
	if p.tty {
		fmt.Printf("\r\033[K  %s", line)
		p.wrote = true
		return
	}
	// Piped: only a change is worth a line, or a download emits thousands.
	if pr.Status != p.lastStat {
		fmt.Printf("  %s\n", line)
		p.lastStat = pr.Status
	}
}

func (p *pullProgress) done() {
	if p.wrote {
		fmt.Println()
	}
}

// pullLine is one progress update as text. Percentage only when the total is
// known, since a bar drawn from a zero total reads as stalled rather than as
// unmeasured.
func pullLine(pr modelpull.Progress) string {
	if pr.Total <= 0 {
		return pr.Status
	}
	pct := float64(pr.Completed) / float64(pr.Total) * 100
	return fmt.Sprintf("%s  %s / %s (%.0f%%)", pr.Status,
		modelpull.HumanBytes(pr.Completed), modelpull.HumanBytes(pr.Total), pct)
}
