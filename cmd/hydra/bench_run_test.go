// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/bench"
	"github.com/ankit373/hydra/internal/bench/suites"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/pricing"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

// benchAnswer answers every Banking77 fixture item with one label; counts calls.
type benchAnswer struct {
	calls  atomic.Int64
	answer string
	err    error
}

func (b *benchAnswer) Execute(_ context.Context, req executor.Request) (*executor.Response, error) {
	b.calls.Add(1)
	if b.err != nil {
		return nil, b.err
	}
	return &executor.Response{Output: b.answer, InputTokens: 100, OutputTokens: 2, Model: req.Head.Meta["model"]}, nil
}

// benchFixture lays out the Banking77 fixture as a fetched data dir with generated
// splits, a lock pinning it, and a config over two OpenRouter arms; every seam is faked.
func benchFixture(t *testing.T, maxUSD string) (args []string, exec *benchAnswer, results string) {
	t.Helper()
	return benchSuiteFixture(t, "banking77", []string{"categories.json", "train.csv", "test.csv"}, "card_arrival", maxUSD)
}

// benchSuiteFixture is benchFixture for any suite with testdata, every call answered with answer.
func benchSuiteFixture(t *testing.T, suite string, files []string, answer, maxUSD string) (args []string, exec *benchAnswer, results string) {
	t.Helper()
	sb := testutil.NewSandbox(t)
	sb.SetKey(t, "OPENROUTER_API_KEY", "test-key")
	src := "../../internal/bench/suites/testdata/" + suite
	root := t.TempDir()
	data := bench.DataDir(root, suite)
	os.MkdirAll(data, 0o755)
	ds := bench.Dataset{Revision: "fixture@1", LicenceVerified: "fixture"}
	for _, n := range files {
		b, err := os.ReadFile(filepath.Join(src, n))
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(data, n), b, 0o644)
		s := sha256.Sum256(b)
		ds.Files = append(ds.Files, bench.LockEntry{Name: n, URL: "http://fixture/" + n, SHA256: hex.EncodeToString(s[:])})
	}
	benchDir := t.TempDir()
	if _, err := suites.GenerateSplits(map[string]bench.Dataset{suite: ds}, root, benchDir); err != nil {
		t.Fatal(err)
	}
	var lock strings.Builder
	fmt.Fprintf(&lock, "[%s]\nrevision = %q\nlicence = \"x\"\nlicence_verified = \"fixture, 2026-11-05\"\n", suite, ds.Revision)
	for _, f := range ds.Files {
		fmt.Fprintf(&lock, "[[%s.file]]\nname = %q\nurl = %q\nsha256 = %q\n", suite, f.Name, f.URL, f.SHA256)
	}
	lockPath := filepath.Join(benchDir, "datasets.lock")
	os.WriteFile(lockPath, []byte(lock.String()), 0o644)
	cfg := `tier = "fixture"
track = "core"
suites = ["` + suite + `"]
n_test = 10
seed = 1
max_usd = ` + maxUSD + `
arms = ["frontier", "cheapest"]
[models]
frontier = "openrouter:vendor/front"
cheapest = "bedrock:amazon.nova-micro-v1:0"
[price_refs]
"amazon.nova-micro-v1:0" = "amazon/nova-micro-v1"
[cutoffs]
"vendor/front" = "2025-01"
"amazon.nova-micro-v1:0" = "unstated"
`
	cfgPath := filepath.Join(benchDir, "fixture.toml")
	os.WriteFile(cfgPath, []byte(cfg), 0o644)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDEXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AWS_REGION", "us-east-1")

	exec = &benchAnswer{answer: answer}
	swap(t, &benchExec, func(provider.Head) executor.Executor { return exec })
	swap(t, &benchPrices, func() (bench.PriceLookup, time.Time) {
		return func(k string) (pricing.ModelPrice, bool) {
			p, ok := map[string]pricing.ModelPrice{
				"vendor/front":         {InputPerMillion: 5, OutputPerMillion: 25},
				"amazon/nova-micro-v1": {InputPerMillion: 0.035, OutputPerMillion: 0.14},
			}[k]
			return p, ok
		}, time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	})
	swap(t, &benchHost, func() bench.HostInfo { return bench.HostInfo{OS: "test", Arch: "test", CPUs: 1} })
	swap(t, &benchSleep, func(context.Context, time.Duration) error { return nil })
	tick := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	swap(t, &benchNow, func() time.Time { tick = tick.Add(time.Second); return tick })
	results = filepath.Join(benchDir, "results")
	return []string{"bench", "run", "--config", cfgPath, "--lock", lockPath, "--data-dir", root,
		"--splits", filepath.Join(benchDir, "splits"), "--results", results}, exec, results
}

func swap[T any](t *testing.T, v *T, to T) {
	t.Helper()
	old := *v
	*v = to
	t.Cleanup(func() { *v = old })
}

func TestBenchRun_DryRunCallsNothing(t *testing.T) {
	args, exec, results := benchFixture(t, "1.0")
	out, _, err := run(t, append(args, "--dry-run")...)
	if err != nil || !strings.Contains(out, "banking77: 4 items × 2 arms (frontier,cheapest) = 8 calls, worst case $") {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if exec.calls.Load() != 0 {
		t.Fatalf("a dry run made %d calls", exec.calls.Load())
	}
	if _, err := os.Stat(results); !os.IsNotExist(err) {
		t.Fatal("a dry run wrote results")
	}
}

func TestBenchRun_RunsResumesAndCommitsTheRunDirectory(t *testing.T) {
	args, exec, results := benchFixture(t, "1.0")
	out, _, err := run(t, args...)
	if err != nil || !strings.Contains(out, "8 rows written") || exec.calls.Load() != 8 {
		t.Fatalf("run: %v after %d calls\n%s", err, exec.calls.Load(), out)
	}
	dirs, _ := os.ReadDir(results)
	if len(dirs) != 1 || !strings.HasPrefix(dirs[0].Name(), "core-banking77-fixture-s1-") {
		t.Fatalf("results = %v", dirs)
	}
	for _, f := range []string{"run.json", "items.jsonl"} {
		if _, err := os.Stat(filepath.Join(results, dirs[0].Name(), f)); err != nil {
			t.Fatal(err)
		}
	}
	if out, _, err = run(t, args...); err != nil || !strings.Contains(out, "0 rows written") || exec.calls.Load() != 8 {
		t.Fatalf("resume of a finished run: %v\n%s", err, out)
	}
	if _, _, err := run(t, append(args, "--suite", "clinc150")...); err == nil {
		t.Fatal("ran a suite the config does not list")
	}
	if _, _, err := run(t, append(args, "--arms", "mid")...); err == nil {
		t.Fatal("ran an arm the config does not define")
	}
}

func TestBenchRun_ExitCodes(t *testing.T) {
	args, _, _ := benchFixture(t, "0.0001") // under one frontier call
	out, _, err := run(t, args...)
	var status exitStatus
	if !errors.As(err, &status) || status != 2 || !strings.Contains(out, "higher --max-usd") {
		t.Fatalf("over the cap: %v\n%s", err, out)
	}

	args, exec, _ := benchFixture(t, "1.0")
	exec.answer = strings.Repeat("x", 10)
	swap(t, &benchExec, func(provider.Head) executor.Executor { return estimating{exec} })
	out, _, err = run(t, args...)
	if !errors.As(err, &status) || status != 4 || !strings.Contains(out, "aborted") {
		t.Fatalf("a contract violation: %v\n%s", err, out)
	}

	args, exec, _ = benchFixture(t, "1.0")
	exec.err = errors.New("boom")
	if _, _, err := run(t, append(args, "--limit", "1", "--seed", "3", "--max-usd", "0.5")...); err != nil {
		t.Fatalf("a transport error leaves the pair unscored, not the run failed: %v", err)
	}
	if _, _, err := run(t, append(args, "--config", filepath.Join(t.TempDir(), "none.toml"))...); err == nil {
		t.Fatal("ran with no config")
	}
}

// estimating reports estimated tokens, which a direct arm must never see.
type estimating struct{ inner *benchAnswer }

func (e estimating) Execute(ctx context.Context, req executor.Request) (*executor.Response, error) {
	r, err := e.inner.Execute(ctx, req)
	if r != nil {
		r.TokensEstimated = true
	}
	return r, err
}

// A GSM8K arm is scored on the final number, like any other suite's arm.
func TestBenchRun_ScoresAGSM8KArm(t *testing.T) {
	args, exec, results := benchSuiteFixture(t, "gsm8k", []string{"test.jsonl"}, "3*12=<<3*12=36>>36\n#### 36", "1.0")
	out, _, err := run(t, args...)
	if err != nil || exec.calls.Load() == 0 {
		t.Fatalf("run: %v after %d calls\n%s", err, exec.calls.Load(), out)
	}
	dirs, _ := os.ReadDir(results)
	if len(dirs) != 1 {
		t.Fatalf("results = %v", dirs)
	}
	b, err := os.ReadFile(filepath.Join(results, dirs[0].Name(), "items.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// Three test items, two arms: only test/0 (gold 36) passes, on both arms.
	rows := string(b)
	if strings.Count(rows, `"passed":true`) != 2 || strings.Count(rows, `"decision":"accept"`) != 6 ||
		!strings.Contains(rows, `"detail":"answered 36, gold 1300"`) {
		t.Fatalf("items.jsonl:\n%s", rows)
	}
}

// An extraction arm is graded field by field: only the item whose gold the answer matches passes.
func TestBenchRun_ScoresAnExtractionArm(t *testing.T) {
	files := []string{"validate_AddToPlaylist.json", "validate_BookRestaurant.json", "validate_GetWeather.json",
		"validate_PlayMusic.json", "validate_RateBook.json", "validate_SearchCreativeWork.json", "validate_SearchScreeningEvent.json"}
	answer := `{"city": "Oslo", "timeRange": "tomorrow"}`
	args, exec, results := benchSuiteFixture(t, "snips", files, answer, "1.0")
	out, _, err := run(t, args...)
	if err != nil || exec.calls.Load() == 0 {
		t.Fatalf("run: %v after %d calls\n%s", err, exec.calls.Load(), out)
	}
	dirs, _ := os.ReadDir(results)
	if len(dirs) != 1 {
		t.Fatalf("results = %v", dirs)
	}
	b, err := os.ReadFile(filepath.Join(results, dirs[0].Name(), "items.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// Six test items, two arms: only GetWeather/0 (city Oslo, timeRange tomorrow) passes, on both arms.
	rows := string(b)
	if strings.Count(rows, `"passed":true`) != 2 || strings.Count(rows, `"decision":"accept"`) != 12 ||
		!strings.Contains(rows, `"detail":"field \"artist\" is missing, gold \"Miles Davis\""`) {
		t.Fatalf("items.jsonl:\n%s", rows)
	}
}
