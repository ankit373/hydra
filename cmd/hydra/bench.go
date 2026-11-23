// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ankit373/hydra/internal/bench"
	"github.com/ankit373/hydra/internal/bench/replay"
	"github.com/ankit373/hydra/internal/bench/suites"
	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/pricing"
	"github.com/ankit373/hydra/internal/sysinfo"
)

// The seams `hyctl bench` reaches the world through, so a test makes no download,
// no provider call and no pricing fetch.
var (
	benchFetcher = bench.DefaultFetcher
	benchExec    = executor.For
	benchNow     = time.Now
	benchSleep   func(context.Context, time.Duration) error // nil: real backoff
	benchPrices  = func() (bench.PriceLookup, time.Time) {
		db := pricing.Load()
		return db.ModelPrice, db.FetchedAt()
	}
	benchHost = func() bench.HostInfo {
		s := sysinfo.Detect()
		return bench.HostInfo{OS: s.OS, Arch: s.Arch, CPUs: runtime.NumCPU(), TotalRAMGB: s.TotalRAMGB,
			FreeRAMGB: s.FreeRAMGB, MemPressure: s.MemPressure.String()}
	}
)

func cmdBench() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bench",
		Short: "Benchmark harness: pinned datasets, committed splits, scored arms",
	}
	cmd.AddCommand(cmdBenchFetch(), cmdBenchRun(), cmdBenchReport(), cmdBenchTable(), cmdBenchReplay())
	return cmd
}

func cmdBenchReport() *cobra.Command {
	var resultsDir string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "report <run-id>",
		Short: "Recompute summary.json from a run's run.json and items.jsonl, and print it",
		Long: "Pure: the same files give a byte-identical summary.json every time. No network.\n" +
			"Verdicts are computed only for a full-tier run.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := bench.Report(filepath.Join(resultsDir, args[0]), benchStateDir())
			if err != nil {
				return err
			}
			if jsonOut {
				b, err := os.ReadFile(filepath.Join(resultsDir, args[0], "summary.json"))
				if err != nil {
					return err
				}
				_, err = stdout.Write(b)
				return err
			}
			fmt.Fprint(stdout, bench.Table([]bench.Summary{s}))
			return nil
		},
	}
	cmd.Flags().StringVar(&resultsDir, "results", "bench/results", "where run directories are")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print summary.json rather than the table")
	return cmd
}

func cmdBenchTable() *cobra.Command {
	var resultsDir, outPath string
	cmd := &cobra.Command{
		Use:   "table <run-id>...",
		Short: "Write the markdown results table from summary.json files only",
		Long: "Reads each run's summary.json and nothing else, and writes one row per (suite, arm)\n" +
			"to --out (default bench/results/<run-id>/table.md for one run). A smoke run is never\n" +
			"a verdict, and a suite whose licence is not verified is marked unpublishable.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if outPath == "" {
				if len(args) > 1 {
					return errors.New("bench: name --out when tabling more than one run")
				}
				outPath = filepath.Join(resultsDir, args[0], "table.md")
			}
			var sums []bench.Summary
			for _, id := range args {
				s, err := bench.ReadSummary(filepath.Join(resultsDir, id, "summary.json"))
				if err != nil {
					return err
				}
				sums = append(sums, s)
			}
			if err := os.WriteFile(outPath, []byte(bench.Table(sums)), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "  wrote %s\n", outPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&resultsDir, "results", "bench/results", "where run directories are")
	cmd.Flags().StringVar(&outPath, "out", "", "the markdown file to write")
	return cmd
}

func cmdBenchReplay() *cobra.Command {
	var candidates, check string
	var policies []string
	var alphas []float64
	var splits int
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "replay --candidates <file.jsonl>",
		Short: "Replay acceptance policies on cached, labelled answers, with no model calls",
		Long: "Splits the tasks 50/50 per seed (seeds 0..--splits-1), calibrates each policy on one\n" +
			"half and runs it on the other. False accept per task is wrong accepted answers over\n" +
			"all test tasks. Each kind is replayed on its own. Row format: bench/README.md.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if splits < 1 {
				return fmt.Errorf("bench: --splits must be at least 1, got %d", splits)
			}
			f, err := os.Open(candidates)
			if err != nil {
				return err
			}
			defer f.Close()
			rows, err := replay.Read(f)
			if err != nil {
				return err
			}
			seeds := make([]uint64, splits)
			for i := range seeds {
				seeds[i] = uint64(i)
			}
			rep, err := replay.Run(rows, replay.Config{Policies: policies, Alphas: alphas, Seeds: seeds, Check: check})
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(stdout).Encode(rep)
			}
			fmt.Fprint(stdout, replay.Table(rep))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&candidates, "candidates", "", "JSONL, one row per (task, head) candidate")
	f.StringSliceVar(&policies, "policy", []string{"always_strongest", "first_check_pass"},
		"policies to replay: "+strings.Join(replay.Names(), ", "))
	f.Float64SliceVar(&alphas, "alpha", []float64{0.01, 0.05, 0.10}, "target false accept per task")
	f.IntVar(&splits, "splits", 20, "random calibrate/test splits, seeded 0..n-1")
	f.StringVar(&check, "check", "", "the primary check first_check_pass accepts on")
	f.BoolVar(&jsonOut, "json", false, "print every split and summary as JSON")
	_ = cmd.MarkFlagRequired("candidates")
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
					fmt.Fprintf(stdout, "  %s/%s  %s  sha256 %s\n", name, e.Name, state, e.SHA256)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&lockPath, "lock", "bench/datasets.lock", "the lockfile pinning each dataset")
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "root for bench/data/<suite> (default $HYDRA_HOME)")
	return cmd
}

func cmdBenchRun() *cobra.Command {
	var cfgPath, suite, arms, lockPath, dataDir, splitsDir, resultsDir string
	var seed uint64
	var maxUSD float64
	var limit int
	var rerun, dryRun bool
	cmd := &cobra.Command{
		Use:   "run --config bench/configs/<tier>.toml",
		Short: "Run the direct arms over a suite's test items, resumably, under a spend cap",
		Long: "Writes bench/results/<run-id>/run.json before the first call, then one row per\n" +
			"(item, arm) to items.jsonl, fsynced. The run id is derived from the config, so\n" +
			"running the same command again resumes. --max-usd is checked before every call.\n\n" +
			"Exit codes: 2 stopped by --max-usd (resume with a higher cap), 4 the contract was\n" +
			"violated mid-run (estimated tokens, over-long output, or a model that moved).",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := bench.ReadConfig(cfgPath)
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("seed") {
				cfg.Seed = seed
			}
			if cmd.Flags().Changed("max-usd") {
				cfg.MaxUSD = maxUSD
			}
			nTest := cfg.NTest
			if limit > 0 && limit < nTest {
				nTest = limit
			}
			names := cfg.Suites
			if suite != "" {
				if !slices.Contains(cfg.Suites, suite) {
					return fmt.Errorf("bench: %s is not one of the config's suites %v", suite, cfg.Suites)
				}
				names = []string{suite}
			}
			var armList []string
			if arms != "" {
				armList = strings.Split(arms, ",")
			}
			lock, err := bench.ReadLock(lockPath)
			if err != nil {
				return err
			}
			if dataDir == "" {
				dataDir = config.Dir()
			}
			overrides, err := cfg.Overrides()
			if err != nil {
				return err
			}
			lookup, fetched := benchPrices()
			prices, err := bench.Snapshot(lookup, fetched, cfg.ModelIDs(), cfg.PriceRefs, overrides)
			if err != nil {
				return err
			}
			host := benchHost()
			out := stdout
			for _, name := range names {
				mk, ok := suites.Registry()[name]
				if !ok {
					return fmt.Errorf("bench: no suite is called %s", name)
				}
				s := mk()
				data := bench.DataDir(dataDir, name)
				splits := filepath.Join(splitsDir, name+".tsv")
				if lock[name].LocalSplits {
					if splits, err = bench.LocalSplitsFile(s, lock[name], data); err != nil {
						return err
					}
				}
				items, err := bench.Load(s, lock[name], data, splits)
				if err != nil {
					return err
				}
				run, byID, err := bench.Plan(cfg, s, lock[name], splits, items, nTest, armList, prices, host)
				if err != nil {
					return err
				}
				if dryRun {
					armMap, err := bench.BuildArms(run, s, benchExec, nil)
					if err != nil {
						return err
					}
					if err := printDryRun(out, s, run, byID, armMap); err != nil {
						return err
					}
					continue
				}
				r := bench.Runner{Run: run, Suite: s, Items: byID, Exec: benchExec, Sleep: benchSleep, MaxUSD: cfg.MaxUSD,
					Rerun: rerun, ResultsDir: resultsDir, StateDir: benchStateDir(), Now: benchNow, Out: out}
				o, err := r.Execute(cmd.Context())
				if o.RunID != "" {
					fmt.Fprintf(out, "  %s: %d rows written, $%.6f spent of $%.2f\n", o.RunID, o.Written, o.Spent, cfg.MaxUSD)
				}
				var ce *bench.ContractError
				switch {
				case errors.Is(err, bench.ErrBudget):
					fmt.Fprintf(out, "  stopped: %v\n  resume with a higher --max-usd\n", err)
					return exitStatus(2)
				case errors.As(err, &ce):
					fmt.Fprintf(out, "  aborted: %v\n", err)
					return exitStatus(4)
				case err != nil:
					return err
				}
				if _, ok := s.(bench.Checker); ok {
					if err := printDetection(out, o.Dir); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&cfgPath, "config", "", "the tier config, e.g. bench/configs/smoke.toml")
	f.StringVar(&suite, "suite", "", "run only this suite of the config's")
	f.StringVar(&arms, "arms", "", "comma-separated arms to run, in rotation order (default: the config's)")
	f.Uint64Var(&seed, "seed", 0, "override the config's seed (execution order and fault plan only)")
	f.Float64Var(&maxUSD, "max-usd", 0, "spend cap per suite run, checked before every call (default: the config's)")
	f.IntVar(&limit, "limit", 0, "run only the first n test items by rank")
	f.BoolVar(&rerun, "rerun", false, "start a fresh -r<k> run rather than resuming (the determinism check)")
	f.BoolVar(&dryRun, "dry-run", false, "print the schedule and its worst-case cost; make no call")
	f.StringVar(&lockPath, "lock", "bench/datasets.lock", "the lockfile pinning each dataset")
	f.StringVar(&dataDir, "data-dir", "", "root for bench/data/<suite> (default $HYDRA_HOME)")
	f.StringVar(&splitsDir, "splits", "bench/splits", "the committed splits directory")
	f.StringVar(&resultsDir, "results", "bench/results", "where run directories are written")
	_ = cmd.MarkFlagRequired("config")
	return cmd
}

// printDryRun prints what a run would call and the most it could cost, calling nothing.
func printDryRun(out io.Writer, s bench.Suite, run bench.Run,
	items map[string]bench.Item, arms map[string]bench.Arm) error {
	var worst float64
	for _, id := range run.ItemIDs {
		req, err := s.Prompt(items[id])
		if err != nil {
			return err
		}
		for _, a := range run.ArmOrder {
			wc, err := arms[a].WorstCaseUSD(req)
			if err != nil {
				return err
			}
			worst += wc
		}
		if g, ok := arms[bench.GraderArm]; ok {
			// One second-model call per answer, so as many as there are arms.
			wc, err := g.WorstCaseUSD(req)
			if err != nil {
				return err
			}
			worst += wc * float64(len(run.ArmOrder))
		}
	}
	grader := ""
	if g, ok := run.Arms[bench.GraderArm]; ok {
		grader = ", plus a second model per answer (" + g.Models[0] + ")"
	}
	fmt.Fprintf(out, "  %s: %d items × %d arms (%s) = %d calls%s, worst case $%.6f; dry run, nothing called\n",
		run.Suite, len(run.ItemIDs), len(run.ArmOrder), strings.Join(run.ArmOrder, ","),
		len(run.ItemIDs)*len(run.ArmOrder), grader, worst)
	return nil
}

// printDetection writes summary.json and prints recall and false flags once a check suite's run is done.
func printDetection(out io.Writer, dir string) error {
	s, err := bench.Report(dir, benchStateDir())
	if err != nil {
		return err
	}
	if t := bench.DetectionTable([]bench.Summary{s}); t != "" && !s.Incomplete {
		fmt.Fprint(out, t)
	}
	return nil
}

// benchStateDir holds each run's lock and state, outside the repository.
func benchStateDir() string { return filepath.Join(config.Dir(), "bench", "runs") }
