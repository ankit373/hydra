// SPDX-License-Identifier: MIT

package main

import (
	"github.com/spf13/cobra"

	"github.com/ankit373/hydra/internal/sandbox"
)

// cmdRlimitExec is the self-re-exec wrapper sandbox.WithLimits builds:
// hidden, since it is never something a person types, only what a dispatched
// head's argv gets rewritten into when a resource ceiling applies. Cobra
// strips the leading "--" itself, so args is already the real target's own
// argv, unaltered. Registered in rootCmd() via root.AddCommand(cmdRlimitExec()).
func cmdRlimitExec() *cobra.Command {
	return &cobra.Command{
		Use:    sandbox.RlimitExecArg,
		Short:  "Internal: apply resource limits, then exec the real command",
		Hidden: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return sandbox.RunRlimitExec(args)
		},
	}
}
