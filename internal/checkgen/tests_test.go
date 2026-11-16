// SPDX-License-Identifier: MIT

package checkgen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/sandbox"
	"github.com/ankit373/hydra/internal/trust"
)

// TestMain lets this binary stand in for hyctl's rlimit wrapper, since
// sandbox.WithLimits re-executes os.Executable() to apply the limits.
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == sandbox.RlimitExecArg {
		fmt.Fprintln(os.Stderr, sandbox.RunRlimitExec(os.Args[3:]))
		os.Exit(1)
	}
	os.Exit(m.Run())
}

const absGo = "package m\n\nfunc Abs(x int) int {\n\tif x < 0 {\n\t\treturn -x\n\t}\n\treturn x\n}\n"

const absNegTest = "package m\n\nimport \"testing\"\n\nfunc TestAbsNegative(t *testing.T) {\n" +
	"\tif Abs(-2) != 2 {\n\t\tt.Fatal(Abs(-2))\n\t}\n}\n"

// goModule is a one-file module in a temp dir; it returns the file's path.
func goModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{"go.mod": "module example.com/m\n\ngo 1.21\n", "abs.go": absGo} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "abs.go")
}

// testsOracleFor runs Generate with a writer that replies files, and builds the oracle.
func testsOracleFor(t *testing.T, file string, wall time.Duration, files ...string) testsOracle {
	t.Helper()
	reply, _ := json.Marshal(files)
	task := Task{Text: "fix Abs", Kind: "code", File: file,
		Ask: func(context.Context, string) (string, error) { return string(reply), nil }}
	specs, err := testsGen{}.Generate(context.Background(), task)
	if err != nil || len(specs) != 1 {
		t.Fatalf("Generate = %v, %v; want one spec", specs, err)
	}
	o, err := testsGen{}.Oracle(specs[0], Env{})
	if err != nil {
		t.Fatal(err)
	}
	to := o.(testsOracle)
	to.wall = wall
	return to
}

// isolated says whether the network can be cut here. Where it cannot, the
// caller checks the not-checked path; CI on Linux and macOS must have it.
func isolated(t *testing.T) bool {
	t.Helper()
	err := sandbox.NetworkIsolation()
	if err != nil && os.Getenv("GITHUB_ACTIONS") == "true" && runtime.GOOS != "windows" {
		t.Fatalf("CI must run generated tests offline: %v", err)
	}
	return err == nil
}

func wantNotIsolated(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, sandbox.ErrNoNetworkIsolation) {
		t.Fatalf("Verify err = %v, want not checked: no network isolation", err)
	}
}

func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(p)
		out[p] = string(raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestTests_RunInCopyNotInTree(t *testing.T) {
	file := goModule(t)
	root := filepath.Dir(file)
	want := snapshot(t, root)
	writes := "package m\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\nfunc TestWritesBeside(t *testing.T) {\n" +
		"\tif err := os.WriteFile(\"touched\", nil, 0o600); err != nil {\n\t\tt.Fatal(err)\n\t}\n}\n"
	o := testsOracleFor(t, file, time.Minute, writes)

	v, err := o.Verify(context.Background(), "```go\npackage m\n\nfunc Abs(x int) int { return x }\n```", trust.Task{})
	if !isolated(t) {
		wantNotIsolated(t, err)
	} else if err != nil || !v.Passed {
		t.Fatalf("Verify = %+v, %v; want the test to run and pass in the copy", v, err)
	}
	got := snapshot(t, root)
	if len(got) != len(want) {
		t.Fatalf("the tree changed: %d files, want %d", len(got), len(want))
	}
	for p, body := range want {
		if got[p] != body {
			t.Errorf("%s changed in the user's tree", p)
		}
	}
}

func TestTests_FailingBeforeAndAfterIsSuspect(t *testing.T) {
	alwaysFails := "package m\n\nimport \"testing\"\n\nfunc TestAlwaysFails(t *testing.T) { t.Fatal(\"wrong\") }\n"
	o := testsOracleFor(t, goModule(t), time.Minute, absNegTest, alwaysFails)

	v, err := o.Verify(context.Background(), absGo, trust.Task{})
	if !isolated(t) {
		wantNotIsolated(t, err)
		return
	}
	if err != nil || !v.Passed || !strings.Contains(v.Detail, "1 dropped as suspect") {
		t.Fatalf("Verify = %+v, %v; want a pass with the always-failing test dropped", v, err)
	}
}

// The done-when case: a change that breaks an edge case the repo has no test for.
func TestTests_BrokenEdgeCaseFails(t *testing.T) {
	o := testsOracleFor(t, goModule(t), time.Minute, absNegTest)

	v, err := o.Verify(context.Background(), "package m\n\nfunc Abs(x int) int { return x }\n", trust.Task{})
	if !isolated(t) {
		wantNotIsolated(t, err)
		return
	}
	if err != nil || v.Passed || !strings.Contains(v.Detail, "TestAbsNegative") {
		t.Fatalf("Verify = %+v, %v; want a fail naming TestAbsNegative", v, err)
	}
}

// A test that dials the host must not reach it, and one that sleeps is stopped at the wall cap.
func TestTests_NetworkAndTimeCapped(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var reached atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			reached.Add(1)
			c.Close()
		}
	}()
	tests := "package m\n\nimport (\n\t\"net\"\n\t\"testing\"\n\t\"time\"\n)\n\n" +
		"func TestDialOut(t *testing.T) {\n\tif c, err := net.Dial(\"tcp\", \"" + ln.Addr().String() + "\"); err == nil {\n" +
		"\t\tc.Close()\n\t\tt.Fatal(\"reached the host\")\n\t}\n}\n\n" +
		"func TestSleeps(t *testing.T) { time.Sleep(time.Hour) }\n"
	const wall = 15 * time.Second
	o := testsOracleFor(t, goModule(t), wall, tests)

	start := time.Now()
	v, err := o.Verify(context.Background(), absGo, trust.Task{})
	if !isolated(t) {
		wantNotIsolated(t, err)
		return
	}
	if took := time.Since(start); took > 3*wall {
		t.Errorf("Verify took %s; the wall cap is %s per run", took, wall)
	}
	if n := reached.Load(); n != 0 {
		t.Errorf("a generated test reached the host %d time(s)", n)
	}
	if err != nil || !v.Passed || !strings.Contains(v.Detail, "1 dropped as suspect") {
		t.Fatalf("Verify = %+v, %v; want the dial blocked and the sleeper dropped", v, err)
	}
}

func TestTests_UnknownLanguageNotChecked(t *testing.T) {
	ask := func(context.Context, string) (string, error) { return "[]", nil }
	for file, want := range map[string]string{"/repo/app.py": "Go only", "": "no file under change"} {
		_, err := testsGen{}.Generate(context.Background(), Task{Text: "x", Kind: "code", File: file, Ask: ask})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Generate(%q) err = %v, want %q", file, err, want)
		}
	}
}

func TestChangedFile(t *testing.T) {
	for in, want := range map[string]string{
		"```go\npackage m\n```": "package m\n",
		"package m\n":           "package m\n",
		"Here:\n```go\nx\n```":  "",
	} {
		got, err := changedFile(in)
		if got != want || (want == "") != (err != nil) {
			t.Errorf("changedFile(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}
