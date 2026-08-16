// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

// cliHead plants a fake CLI binary that always prints stdout, regardless of
// the args the executor's template builds — same technique as fakeAgy.
func cliHead(t *testing.T, s *testutil.Sandbox, id, provName, stdout string) provider.Head {
	t.Helper()
	body := "#!/bin/sh\nprintf '%s' " + shellQuote(stdout) + "\n"
	if runtime.GOOS == "windows" {
		body = "@echo off\r\necho " + stdout + "\r\n"
	}
	return provider.Head{
		ID: id, Name: id, Provider: provName, Source: "cli",
		Executable: s.FakeBinary(t, id, body),
	}
}

// CLI tools (claude, codex, cursor, ...) never report real token usage. Without
// an estimate here, dispatch's logDispatch/recordBudget gate on InputTokens>0
// and silently drop every CLI-driven dispatch from cost.jsonl and the budget
// registry (#502).
func TestCLIExecute_TokensAreEstimatedAndLabelled(t *testing.T) {
	s := testutil.NewSandbox(t)
	head := cliHead(t, s, "codex", "openai", "0123456789012345678901234567890123456789")

	resp, err := (&CLIExecutor{}).Execute(context.Background(), Request{
		Prompt: strings.Repeat("a", 400), Head: head,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.TokensEstimated {
		t.Error("TokensEstimated = false; CLI heads report no usage, so these are " +
			"char/4 guesses and must never be booked as measured spend")
	}
	if resp.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 400/4", resp.InputTokens)
	}
	if resp.OutputTokens != 10 {
		t.Errorf("OutputTokens = %d, want 40/4", resp.OutputTokens)
	}
}

func TestCLIExecute_OutputAndModelAreSet(t *testing.T) {
	s := testutil.NewSandbox(t)
	head := cliHead(t, s, "codex", "openai", "the answer")

	resp, err := (&CLIExecutor{}).Execute(context.Background(), Request{
		Prompt: "hello", Head: head,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Output != "the answer" {
		t.Errorf("Output = %q", resp.Output)
	}
	if resp.Model != "codex" {
		t.Errorf("Model = %q, want head ID", resp.Model)
	}
}
