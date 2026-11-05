// SPDX-License-Identifier: MIT

package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ankit373/hydra/internal/bench"
	"github.com/ankit373/hydra/internal/config"
)

// benchFetcher is a var so a test can point fetch at an httptest server.
var benchFetcher = bench.DefaultFetcher

func cmdBench() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bench",
		Short: "Benchmark harness: pinned datasets, committed splits, scored arms",
	}
	cmd.AddCommand(cmdBenchFetch())
	return cmd
}

func cmdBenchFetch() *cobra.Command {
	var lockPath, dataDir string
	cmd := &cobra.Command{
		Use:   "fetch [suite...]",
		Short: "Download each pinned dataset file and verify its sha256",
		Long: "Downloads every file bench/datasets.lock pins into $HYDRA_HOME/bench/data/<suite>/\n" +
			"(or --data-dir), outside the repository. A file already present with the pinned\n" +
			"hash is not downloaded again; a mismatch is refused, never fixed up.",
		RunE: func(cmd *cobra.Command, args []string) error {
			lock, err := bench.ReadLock(lockPath)
			if err != nil {
				return err
			}
			names := args
			if len(names) == 0 {
				names = bench.Names(lock)
			}
			if dataDir == "" {
				dataDir = config.Dir()
			}
			f := benchFetcher()
			for _, name := range names {
				ds, ok := lock[name]
				if !ok {
					return fmt.Errorf("bench: %s is not in %s", name, lockPath)
				}
				for _, e := range ds.Files {
					got, err := f.Fetch(cmd.Context(), bench.DataDir(dataDir, name), e)
					if err != nil {
						return err
					}
					state := "verified"
					if got {
						state = "downloaded"
					}
					fmt.Fprintf(cmd.OutOrStdout(), "  %s/%s  %s  sha256 %s\n", name, e.Name, state, e.SHA256)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&lockPath, "lock", "bench/datasets.lock", "the lockfile pinning each dataset")
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "root for bench/data/<suite> (default $HYDRA_HOME)")
	return cmd
}
