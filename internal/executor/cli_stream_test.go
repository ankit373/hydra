// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

// A CLI head streams by pointing cmd.Stdout at the sink, so the child's output
// reaches a surface as exec copies it rather than only at exit.
func TestCLIStream_ForwardsChildStdoutAndKeepsTheResponseIdentical(t *testing.T) {
	s := testutil.NewSandbox(t)
	head := cliHead(t, s, "codex", "openai", "the streamed answer")

	var got []string
	resp, err := (&CLIExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "hello", Head: head}, func(d string) { got = append(got, d) })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("no deltas: the child's stdout never reached the callback")
	}
	// The deltas are the raw stream, the Response is trimmed, exactly as
	// Execute trims it. Both must describe the same answer.
	if strings.TrimSpace(strings.Join(got, "")) != resp.Output {
		t.Errorf("deltas %q do not reassemble to Output %q", got, resp.Output)
	}
	if resp.Output != "the streamed answer" {
		t.Errorf("Output %q", resp.Output)
	}
	if !resp.TokensEstimated {
		t.Error("a CLI head reports no usage, so its counts must stay labelled estimated")
	}
	if resp.TTFT == 0 {
		t.Error("TTFT is zero though output was observed arriving")
	}
}

// A head with no template is unrunnable, and both paths have to say so the
// same way rather than one erroring and the other answering.
func TestCLIStream_NoTemplateIsRefusedLikeExecute(t *testing.T) {
	head := provider.Head{
		ID: "not-a-cli", Provider: "nowhere", Executable: "/nonexistent",
		Meta: map[string]string{},
	}
	req := Request{Prompt: "p", Head: head}

	_, streamErr := (&CLIExecutor{}).ExecuteStream(context.Background(), req, func(string) {})
	_, execErr := (&CLIExecutor{}).Execute(context.Background(), req)
	if streamErr == nil || execErr == nil {
		t.Fatalf("expected both paths to refuse: stream=%v exec=%v", streamErr, execErr)
	}
	if streamErr.Error() != execErr.Error() {
		t.Errorf("the two paths refuse differently:\n  stream: %v\n  exec:   %v", streamErr, execErr)
	}
}

// Azure puts the deployment in the path and no model in the body, so a wrong
// URL here fails at request time against a real endpoint and nowhere else.
func TestAzureStreamTarget_BuildsTheDeploymentURL(t *testing.T) {
	t.Setenv("AZURE_OPENAI_ENDPOINT", "https://example.openai.azure.com/")
	t.Setenv("AZURE_OPENAI_DEPLOYMENT", "my deployment")
	t.Setenv("AZURE_OPENAI_API_KEY", "secret")

	endpoint, model, headers, err := azureStreamTarget(Request{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(endpoint, "https://example.openai.azure.com/openai/deployments/") {
		t.Errorf("endpoint %q: base was not preserved or the trailing slash doubled", endpoint)
	}
	// A deployment name with a space has to be escaped, not sent raw.
	if !strings.Contains(endpoint, "my%20deployment") {
		t.Errorf("endpoint %q did not escape the deployment name", endpoint)
	}
	if !strings.Contains(endpoint, "api-version=") {
		t.Errorf("endpoint %q carries no api-version", endpoint)
	}
	if model != "" {
		t.Errorf("model %q: Azure names the model by deployment path, not in the body", model)
	}
	if headers["api-key"] != "secret" {
		t.Errorf("api-key header is %q", headers["api-key"])
	}
}
