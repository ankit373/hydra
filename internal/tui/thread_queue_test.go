// SPDX-License-Identifier: MIT

package tui

// Queue-on-overlap (#598): same-file edits never run concurrently; the second
// queues visibly and auto-starts on apply/discard. Vector clocks are the
// apply-time backstop. The race test at the bottom runs real workers
// concurrently, the detector must see the actual parallel paths.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ankit373/hydra/internal/a2a"
	"github.com/ankit373/hydra/internal/editor"
	"github.com/ankit373/hydra/internal/graph"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/testutil"
)

// stubThreadEdit writes tag-stamped content to whatever file the task targets.
func stubThreadEdit(delay time.Duration) func(context.Context, *ckTask, string) (*editor.Result, error) {
	return func(ctx context.Context, tk *ckTask, _ string) (*editor.Result, error) {
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		before, _ := os.ReadFile(tk.file)
		after := fmt.Sprintf("package pkg // edited by thread %d\n", tk.threadID)
		if err := os.WriteFile(tk.file, []byte(after), 0o644); err != nil {
			return nil, err
		}
		runlog.LogEdit(tk.runID, tk.taskID, tk.file, before, []byte(after), 1, 1)
		return &editor.Result{Status: "ok", File: tk.file, LinesAdded: 1, LinesRemoved: 1, Head: "stub"}, nil
	}
}

func TestQueue_SameFileQueuesThenAutoStartsOnApply(t *testing.T) {
	repo := threadRepo(t)
	stubExec(t)
	ckEditStage = stubThreadEdit(0)

	m := chatFixture("edit")
	m.repoRoot = repo
	m2, cmd := m.startTask("edit pkg/main.go first")
	m = runCmds(t, m2, cmd)
	if m.th().wt == nil {
		t.Fatal("thread 1 got no worktree")
	}

	m = press(m, tea.KeyCtrlT)
	m2, cmd = m.startTask("edit pkg/main.go second")
	m = runCmds(t, m2, cmd)
	th2 := m.th()
	if th2.queued == nil {
		t.Fatal("the second edit on the same file did not queue")
	}
	if th2.status() != "queued" {
		t.Errorf("status = %q", th2.status())
	}
	reason := stripANSI(th2.queued.reason)
	if !strings.Contains(reason, "queued behind 1") || !strings.Contains(reason, "both touch pkg/main.go") {
		t.Errorf("reason = %q", reason)
	}
	// The strip shows the queued glyph; the header line carries the reason.
	if !strings.Contains(stripANSI(m.threadStrip(120)), "◱") {
		t.Error("the strip does not show the queued chip")
	}
	if hdr := stripANSI(th2.threadHeaderLine(120)); !strings.Contains(hdr, "queued behind 1") {
		t.Errorf("header = %q", hdr)
	}

	// Blocker applies → the queued thread auto-starts and lands its own edit.
	m = altDigit(m, '1')
	m, acmd := keyRune(m, 'a')
	m = runCmds(t, m, acmd)
	th2 = m.threadByID(2)
	if th2.queued != nil {
		t.Fatal("apply did not release the queued thread")
	}
	if th2.lastDone == nil || !th2.lastDone.edited {
		t.Fatalf("the released thread did not run: %+v", th2.lastDone)
	}
	logs := stripANSI(strings.Join(th2.log, "\n"))
	if !strings.Contains(logs, "unblocked, starting") {
		t.Errorf("the release is not visible: %q", logs)
	}
	// Thread 2 ran AFTER thread 1 applied: its clock must dominate thread 1's,
	// not read as concurrent, that is the causal-ordering merge.
	if got := th2.clock.Compare(m.threadByID(1).clock); got != a2a.After {
		t.Errorf("released thread's clock ordering = %v, want After", got)
	}
}

func TestQueue_DiscardAlsoReleases(t *testing.T) {
	repo := threadRepo(t)
	stubExec(t)
	ckEditStage = stubThreadEdit(0)

	m := chatFixture("edit")
	m.repoRoot = repo
	m2, cmd := m.startTask("edit pkg/other.go one")
	m = runCmds(t, m2, cmd)
	m = press(m, tea.KeyCtrlT)
	m2, cmd = m.startTask("edit pkg/other.go two")
	m = runCmds(t, m2, cmd)
	if m.th().queued == nil {
		t.Fatal("no queue")
	}

	m = altDigit(m, '1')
	m, _ = keyRune(m, 'x') // arm
	m, dcmd := keyRune(m, 'x')
	m = runCmds(t, m, dcmd)
	th2 := m.threadByID(2)
	if th2.queued != nil || th2.lastDone == nil {
		t.Fatalf("discard did not release the queue: queued=%v done=%v", th2.queued, th2.lastDone)
	}
}

func TestQueue_ChainRequeuesBehindTheNewBlocker(t *testing.T) {
	repo := threadRepo(t)
	stubExec(t)
	ckEditStage = stubThreadEdit(0)

	m := chatFixture("edit")
	m.repoRoot = repo
	m2, cmd := m.startTask("edit pkg/third.go a")
	m = runCmds(t, m2, cmd)
	m = press(m, tea.KeyCtrlT)
	m2, cmd = m.startTask("edit pkg/third.go b")
	m = runCmds(t, m2, cmd)
	m = press(m, tea.KeyCtrlT)
	m2, cmd = m.startTask("edit pkg/third.go c")
	m = runCmds(t, m2, cmd)

	if m.threadByID(2).queued == nil || m.threadByID(3).queued == nil {
		t.Fatal("the chain did not queue")
	}
	// Thread 1 applies: 2 starts, 3 requeues behind 2.
	m = altDigit(m, '1')
	m, acmd := keyRune(m, 'a')
	m = runCmds(t, m, acmd)
	if m.threadByID(2).queued != nil {
		t.Fatal("thread 2 did not start")
	}
	q3 := m.threadByID(3).queued
	if q3 == nil {
		t.Fatal("thread 3 started alongside thread 2, same file")
	}
	if q3.blockerID != 2 {
		t.Errorf("thread 3 queues behind %d, want 2", q3.blockerID)
	}
}

// Degraded mode: no git repo → no isolation exists, so edits are serial even
// on different files, and the degradation is said out loud.
func TestQueue_NoRepoSerializesEditsInPlace(t *testing.T) {
	testutil.NewSandbox(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(dir, "pkg", f), []byte("package pkg\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prev, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	stubExec(t)
	edits := make(chan int, 4)
	ckEditStage = func(_ context.Context, tk *ckTask, _ string) (*editor.Result, error) {
		edits <- tk.threadID
		runlog.LogEdit(tk.runID, tk.taskID, tk.file, []byte("a"), []byte("b"), 1, 0)
		return &editor.Result{Status: "ok", File: tk.file, LinesAdded: 1, Head: "stub"}, nil
	}

	m := chatFixture("edit")
	m.repoRoot = "" // not a git repo
	// Hold thread 1's edit open by hand so the second submit sees it running.
	th1 := m.th()
	th1.inEdit = true
	th1.files = []string{filepath.Join(dir, "pkg", "a.go")}
	th1.exec = &ckExecState{stage: "editing"}
	logs := stripANSI(strings.Join(th1.log, "\n"))
	_ = logs

	m = press(m, tea.KeyCtrlT)
	m2, cmd := m.startTask("edit pkg/b.go too")
	m = runCmds(t, m2, cmd)
	if m.th().queued == nil {
		t.Fatal("a second in-place edit ran concurrently with the first")
	}
	if !strings.Contains(stripANSI(m.th().queued.reason), "no git repo") {
		t.Errorf("the degradation is not named: %q", m.th().queued.reason)
	}

	// The blocker finishing releases it.
	ex := th1.exec
	next, rcmd := m.Update(ckExecDoneMsg{exec: ex, task: ckTask{threadID: 1, runID: "r1", elapsed: time.Second}})
	m = runCmds(t, next.(Cockpit), rcmd)
	if m.threadByID(2).queued != nil {
		t.Fatal("the blocker's completion did not release the queue")
	}
	if got := <-edits; got != 2 {
		t.Errorf("the released edit belonged to thread %d", got)
	}
	warn := stripANSI(strings.Join(m.threadByID(2).log, "\n"))
	if !strings.Contains(warn, "no isolation") {
		t.Errorf("the in-place warning is missing: %q", warn)
	}
}

// Graph coupling queues indirect collisions when a graph is loaded.
func TestQueue_GraphCoupledTargetsQueue(t *testing.T) {
	repo := threadRepo(t)
	stubExec(t)
	ckEditStage = stubThreadEdit(0)

	gpath := filepath.Join(t.TempDir(), "graph.json")
	data := `{"nodes":[{"id":"m","file":"pkg/main.go"},{"id":"o","file":"pkg/other.go"}],"edges":[{"from":"o","to":"m"}]}`
	if err := os.WriteFile(gpath, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := graph.Load(gpath)
	if err != nil {
		t.Fatal(err)
	}

	m := chatFixture("edit")
	m.repoRoot = repo
	m.metrics.graph = g
	m2, cmd := m.startTask("edit pkg/main.go base")
	m = runCmds(t, m2, cmd)
	m = press(m, tea.KeyCtrlT)
	m2, cmd = m.startTask("edit pkg/other.go dependent")
	m = runCmds(t, m2, cmd)
	q := m.th().queued
	if q == nil {
		t.Fatal("graph-coupled targets ran concurrently")
	}
	if !strings.Contains(stripANSI(q.reason), "coupled") {
		t.Errorf("reason = %q", q.reason)
	}
}

// The a2a backstop: concurrent clocks + overlapping files warn at apply;
// causally ordered clocks stay silent.
func TestConcurrentWarnings_VectorClockBackstop(t *testing.T) {
	m := testCockpit()
	m = press(m, tea.KeyCtrlT)
	t1, t2 := m.threadByID(1), m.threadByID(2)
	t1.files = []string{"pkg/main.go"}
	t2.files = []string{"pkg/main.go"}
	// Independent ticks → concurrent.
	t1.clock = a2a.Clock{}.Tick(ckThreadAgent(1))
	t2.clock = a2a.Clock{}.Tick(ckThreadAgent(2))
	warns := m.concurrentWarnings(t1)
	if len(warns) != 1 || !strings.Contains(stripANSI(warns[0]), "thread 2 also touched pkg/main.go") {
		t.Fatalf("warns = %v", warns)
	}
	// Merge (the release path) orders them: no warning.
	t2.clock = t2.clock.Merge(t1.clock).Tick(ckThreadAgent(2))
	if warns := m.concurrentWarnings(t1); len(warns) != 0 {
		t.Errorf("ordered clocks still warn: %v", warns)
	}
	// Disjoint files: concurrent but no overlap, no warning.
	t2.clock = a2a.Clock{}.Tick(ckThreadAgent(2))
	t2.files = []string{"pkg/other.go"}
	if warns := m.concurrentWarnings(t1); len(warns) != 0 {
		t.Errorf("disjoint files warned: %v", warns)
	}
}

// ── concurrency: real workers, run in parallel ───────────────────────────────

// runRounds executes cmd trees in concurrent rounds: every cmd in a round runs
// in its own goroutine (the workers genuinely race); the resulting messages
// then apply to the model sequentially, exactly as Bubble Tea would.
func runRounds(t *testing.T, m Cockpit, cmds []tea.Cmd) Cockpit {
	t.Helper()
	for round := 0; len(cmds) > 0; round++ {
		if round > 20 {
			t.Fatal("cmd rounds did not converge")
		}
		var mu sync.Mutex
		var msgs []tea.Msg
		var wg sync.WaitGroup
		var run func(c tea.Cmd)
		run = func(c tea.Cmd) {
			defer wg.Done()
			if c == nil {
				return
			}
			switch msg := c().(type) {
			case tea.BatchMsg:
				for _, cc := range msg {
					wg.Add(1)
					go run(cc)
				}
			case ckSpinTickMsg, ckCodeTickMsg:
			default:
				mu.Lock()
				msgs = append(msgs, msg)
				mu.Unlock()
			}
		}
		for _, c := range cmds {
			wg.Add(1)
			go run(c)
		}
		wg.Wait()
		cmds = nil
		for _, msg := range msgs {
			next, cmd := m.Update(msg)
			m = next.(Cockpit)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}
	return m
}

// Three edit threads on three different files run their pipelines
// CONCURRENTLY: separate worktrees, real runlog heartbeats, stubbed editors
// with real sleeps, and every result must land on its own thread.
func TestThreads_ConcurrentWorkersSettleOnTheirOwnThreads(t *testing.T) {
	repo := threadRepo(t)
	stubExec(t)
	ckEditStage = stubThreadEdit(30 * time.Millisecond)

	m := chatFixture("edit")
	m.repoRoot = repo
	var cmds []tea.Cmd
	for i, f := range []string{"pkg/main.go", "pkg/other.go", "pkg/third.go"} {
		if i > 0 {
			m = press(m, tea.KeyCtrlT)
		}
		m2, cmd := m.startTask("edit " + f + " concurrently")
		m = m2
		if cmd == nil {
			t.Fatalf("thread %d did not launch", i+1)
		}
		cmds = append(cmds, cmd)
	}
	if _, _, queued := m.threadCounts(); queued != 0 {
		t.Fatal("disjoint files queued, they must run concurrently")
	}

	m = runRounds(t, m, cmds)

	for id := 1; id <= 3; id++ {
		th := m.threadByID(id)
		if th.lastDone == nil || !th.lastDone.edited {
			t.Fatalf("thread %d did not settle: %+v", id, th.lastDone)
		}
		if th.lastDone.threadID != id {
			t.Errorf("thread %d holds thread %d's task", id, th.lastDone.threadID)
		}
		if th.wt == nil {
			t.Fatalf("thread %d lost its worktree", id)
		}
		raw, err := os.ReadFile(filepath.Join(th.wt.dir, "pkg", []string{"main.go", "other.go", "third.go"}[id-1]))
		if err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("edited by thread %d", id)
		if !strings.Contains(string(raw), want) {
			t.Errorf("thread %d's worktree holds %q, want %q", id, raw, want)
		}
	}

	// All three apply cleanly, disjoint files cannot conflict.
	for id := 1; id <= 3; id++ {
		m = altDigit(m, rune('0'+id))
		var acmd tea.Cmd
		m, acmd = keyRune(m, 'a')
		m = runCmds(t, m, acmd)
		if m.threadByID(id).wt != nil {
			t.Errorf("thread %d's apply left its worktree", id)
		}
	}
	for i, f := range []string{"main.go", "other.go", "third.go"} {
		raw, _ := os.ReadFile(filepath.Join(repo, "pkg", f))
		want := fmt.Sprintf("edited by thread %d", i+1)
		if !strings.Contains(string(raw), want) {
			t.Errorf("%s = %q, want %q", f, raw, want)
		}
	}
}
