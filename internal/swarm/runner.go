package swarm

import (
	"context"
	"sync"
	"time"

	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/provider"
)

// executeHead runs one head and returns a completed Attempt.
// Never returns an error — all failures are captured in Attempt.Status/Err.
func executeHead(ctx context.Context, h provider.Head, prompt string, opts Options) Attempt {
	a := Attempt{
		Head:      h,
		Status:    StatusRunning,
		StartedAt: time.Now(),
	}

	execCtx := ctx
	var cancel context.CancelFunc
	if opts.PerHeadTimeout > 0 {
		execCtx, cancel = context.WithTimeout(ctx, opts.PerHeadTimeout)
		defer cancel()
	}

	exec := executor.For(h)
	resp, err := exec.Execute(execCtx, executor.Request{
		Prompt:    prompt,
		Head:      h,
		MaxTokens: opts.MaxTokens,
		System:    opts.System,
	})

	a.FinishedAt = time.Now()
	a.Duration = a.FinishedAt.Sub(a.StartedAt)

	if err != nil {
		a.Status = classifyError(err)
		a.Err = err
		return a
	}

	a.Status = StatusOK
	a.Output = resp.Output
	a.InputTokens = resp.InputTokens
	a.OutputTokens = resp.OutputTokens
	a.TokensEstimated = resp.TokensEstimated
	a.Truncated = resp.Truncated
	return a
}

// runRace fires all heads concurrently and returns as soon as the first
// StatusOK attempt arrives. All other goroutines are canceled and drained
// before returning — no goroutine leaks, no zombie subprocesses.
func runRace(ctx context.Context, heads []provider.Head, prompt string, opts Options) []Attempt {
	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	attempts := make([]Attempt, len(heads))
	var mu sync.Mutex
	won := false

	var wg sync.WaitGroup
	for i, h := range heads {
		i, h := i, h
		wg.Add(1)
		go func() {
			defer wg.Done()
			a := executeHead(raceCtx, h, prompt, opts)

			mu.Lock()
			defer mu.Unlock()

			if a.Status == StatusOK && !won {
				won = true
				cancel() // signal all other goroutines to stop
				a.Rank = 1
			}
			attempts[i] = a
		}()
	}

	wg.Wait()

	// Any attempt that didn't finish before cancel is marked Canceled.
	mu.Lock()
	defer mu.Unlock()
	for i := range attempts {
		if attempts[i].Status == StatusRunning || attempts[i].Status == StatusPending {
			attempts[i].Status = StatusCanceled
			attempts[i].Head = heads[i]
		}
	}
	return attempts
}

// runAll fires all heads concurrently and waits for every one to finish.
// Used by both ModeAll (rank by CapScore) and ModeBest (feed to judge).
// Uses sync.WaitGroup (not errgroup) — guarantees goroutine drain regardless
// of errors, preventing zombie agy subprocesses.
func runAll(ctx context.Context, heads []provider.Head, prompt string, opts Options) []Attempt {
	attempts := make([]Attempt, len(heads))
	var mu sync.Mutex
	var wg sync.WaitGroup

	for i, h := range heads {
		i, h := i, h
		wg.Add(1)
		go func() {
			defer wg.Done()
			a := executeHead(ctx, h, prompt, opts)
			mu.Lock()
			attempts[i] = a
			mu.Unlock()
		}()
	}
	wg.Wait()
	return attempts
}
