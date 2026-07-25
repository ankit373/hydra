// SPDX-License-Identifier: MIT

package executor

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// CLIExecutor runs a prompt via a subprocess CLI tool.
type CLIExecutor struct{}

func (e *CLIExecutor) Execute(ctx context.Context, req Request) (*Response, error) {
	tmpl, ok := cliTemplates[req.Head.Provider]
	if !ok {
		tmpl, ok = cliTemplates[req.Head.ID]
	}
	if !ok {
		return nil, fmt.Errorf("no CLI template for head %q (provider %q)", req.Head.ID, req.Head.Provider)
	}

	args := tmpl.buildArgs(req.Prompt)
	cmd := exec.CommandContext(ctx, req.Head.Executable, args...)

	if tmpl.stdinPrompt {
		cmd.Stdin = strings.NewReader(req.Prompt)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("cli exec %s: %w — stderr: %s", req.Head.ID, err, stderr.String())
	}

	return &Response{
		Output:   strings.TrimSpace(stdout.String()),
		Duration: time.Since(start),
		Model:    req.Head.ID,
	}, nil
}

// cliTemplate describes how to invoke a specific AI CLI tool.
type cliTemplate struct {
	args        []string // positional args before the prompt; use "" as prompt placeholder
	stdinPrompt bool     // if true, prompt is passed via stdin instead of args
}

func (t cliTemplate) buildArgs(prompt string) []string {
	var out []string
	for _, a := range t.args {
		if a == "" {
			out = append(out, prompt)
		} else {
			out = append(out, a)
		}
	}
	return out
}

// cliTemplates maps provider ID (or head ID) to invocation template.
// To add a new CLI tool, add an entry here and in capabilities/data.json.
var cliTemplates = map[string]cliTemplate{
	"anthropic":   {args: []string{"--print", ""}}, // claude --print "<prompt>"
	"openai":      {args: []string{""}},            // codex "<prompt>"
	"google":      {args: []string{""}},            // gemini "<prompt>"
	"antigravity": {args: []string{""}},            // agy "<prompt>"
	"cursor":      {args: []string{"--stdio"}, stdinPrompt: true},
	"amazon":      {args: []string{""}},                       // kiro "<prompt>"
	"codeium":     {args: []string{""}},                       // windsurf "<prompt>"
	"github":      {args: []string{"copilot", "suggest", ""}}, // gh-copilot
	"sourcegraph": {args: []string{"ask", ""}},                // cody ask "<prompt>"
	"continue":    {stdinPrompt: true},
	"amp":         {args: []string{""}},
}
