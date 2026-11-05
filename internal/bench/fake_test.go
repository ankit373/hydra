// SPDX-License-Identifier: MIT

package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/provider"
)

// fakeSuite is a label suite whose prompt is the item text, graded by exact match.
type fakeSuite struct{}

func (fakeSuite) Name() string                        { return "fake" }
func (fakeSuite) Class() string                       { return "classification.fake" }
func (fakeSuite) Rows(Dataset, string) ([]Row, error) { return nil, nil }
func (fakeSuite) Labels() []string                    { return []string{"yes", "no"} }
func (fakeSuite) Prompt(it Item) (executor.Request, error) {
	text, err := it.Text()
	zero := 0.0
	return executor.Request{Prompt: text, MaxTokens: 32, Temperature: &zero}, err
}
func (fakeSuite) Grade(_ context.Context, it Item, answer string) (Grade, error) {
	gold, _ := it.Label()
	if answer != "yes" && answer != "no" {
		return Grade{Format: true, Detail: "not a label"}, nil
	}
	return Grade{Passed: answer == gold}, nil
}

// fakeItems are n test items "q000"…, gold "yes" on even ones.
func fakeItems(n int) map[string]Item {
	out := map[string]Item{}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("test/%03d", i)
		in, _ := json.Marshal(map[string]string{"text": fmt.Sprintf("q%03d", i)})
		gold := "no"
		if i%2 == 0 {
			gold = "yes"
		}
		g, _ := json.Marshal(map[string]string{"label": gold})
		out[id] = Item{ID: id, Class: "classification.fake", Split: SplitTest, Rank: Rank("fake", id), Input: in, Gold: g}
	}
	return out
}

// reply is one scripted answer; a nil err with an empty answer is a real empty answer.
type reply struct {
	answer    string
	err       error
	out       int
	model     string
	estimated bool
}

// fakeExec answers by head; script overrides per (head, prompt). Every call is
// counted, per head too, and before can cancel a context mid-call.
type fakeExec struct {
	mu      sync.Mutex
	script  map[string]reply // "<head>|<prompt>" or "<head>|*"
	calls   atomic.Int64
	perHead map[string]int
	before  func(head, prompt string)
}

func newFakeExec() *fakeExec { return &fakeExec{script: map[string]reply{}, perHead: map[string]int{}} }

func (f *fakeExec) set(head, prompt string, r reply) {
	f.mu.Lock()
	f.script[head+"|"+prompt] = r
	f.mu.Unlock()
}

func (f *fakeExec) count(head string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.perHead[head]
}

func (f *fakeExec) For(provider.Head) executor.Executor { return f }

func (f *fakeExec) Execute(ctx context.Context, req executor.Request) (*executor.Response, error) {
	f.calls.Add(1)
	f.mu.Lock()
	f.perHead[req.Head.ID]++
	r, ok := f.script[req.Head.ID+"|"+req.Prompt]
	if !ok {
		r, ok = f.script[req.Head.ID+"|*"]
	}
	f.mu.Unlock()
	if f.before != nil {
		f.before(req.Head.ID, req.Prompt)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !ok { // default: right on even items, wrong on odd, by the item number
		r.answer = "yes"
		if n := strings.TrimPrefix(req.Prompt, "q"); n != "" && (n[len(n)-1]-'0')%2 == 1 {
			r.answer = "no"
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	model := r.model
	if model == "" {
		model = req.Head.Meta["model"]
	}
	return &executor.Response{Output: r.answer, InputTokens: inputTokens(req), OutputTokens: r.out,
		Model: model, TokensEstimated: r.estimated}, nil
}

// inputTokens reports exactly the harness's worst-case estimate, so with a zero
// output price a call's actual cost equals its reservation.
func inputTokens(req executor.Request) int { return (len(req.System)+len(req.Prompt))/3 + 1 }

// testPrices: $0.001 per input token, output free, so q000 (2 tokens) costs $0.002.
func testPrices(models ...string) PriceSnapshot {
	s := PriceSnapshot{}
	for _, m := range models {
		s[m] = Price{Model: m, Ref: "ref/" + m, InPerM: 1000, Source: "openrouter", FetchedAt: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)}
	}
	return s
}

// stepClock advances one second per reading, so every call has a one-second latency
// whatever ran before it.
func stepClock() func() time.Time {
	var mu sync.Mutex
	t := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		t = t.Add(time.Second)
		return t
	}
}
