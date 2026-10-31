// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

type bedrockStub struct {
	*httptest.Server
	header http.Header
	paths  []string
}

// newBedrockStub points both Bedrock paths at itself through
// AWS_ENDPOINT_URL_BEDROCK_RUNTIME, the variable the AWS SDKs read.
func newBedrockStub(t *testing.T, frames [][]byte) *bedrockStub {
	t.Helper()
	s := &bedrockStub{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		s.header = r.Header.Clone()
		s.paths = append(s.paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		fl, _ := w.(http.Flusher)
		for _, f := range frames {
			_, _ = w.Write(f)
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	t.Cleanup(s.Server.Close)

	sb := testutil.NewSandbox(t)
	sb.SetKey(t, "AWS_ACCESS_KEY_ID", "AKIDEXAMPLE")
	sb.SetKey(t, "AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AWS_REGION", "eu-west-1")
	t.Setenv("AWS_ENDPOINT_URL_BEDROCK_RUNTIME", s.Server.URL)
	t.Setenv("BEDROCK_MODEL_ID", "anthropic.claude-3-5-sonnet-20241022-v2:0")
	return s
}

func bedrockHead() provider.Head {
	return provider.Head{
		ID: "bedrock", Name: "Amazon Bedrock", Provider: "bedrock",
		Source: "env", AuthReady: true, Meta: map[string]string{},
	}
}

func bedrockDelta(text string) []byte {
	return encodeEventFrame(
		map[string]string{":message-type": "event", ":event-type": "contentBlockDelta"},
		[]byte(`{"contentBlockIndex":0,"delta":{"text":`+quoteJSON(text)+`}}`))
}

func quoteJSON(s string) string {
	return `"` + strings.NewReplacer(`"`, `\"`, "\n", `\n`).Replace(s) + `"`
}

var bedrockMetadata = encodeEventFrame(
	map[string]string{":message-type": "event", ":event-type": "metadata"},
	[]byte(`{"usage":{"inputTokens":44,"outputTokens":13,"totalTokens":57},"metrics":{"latencyMs":812}}`))

func TestBedrockStream_ReassemblesDeltasAndReportsMetadataCounts(t *testing.T) {
	stub := newBedrockStub(t, [][]byte{
		encodeEventFrame(map[string]string{":message-type": "event", ":event-type": "messageStart"},
			[]byte(`{"role":"assistant"}`)),
		bedrockDelta("Converse "),
		bedrockDelta("streams."),
		encodeEventFrame(map[string]string{":message-type": "event", ":event-type": "contentBlockStop"},
			[]byte(`{"contentBlockIndex":0}`)),
		encodeEventFrame(map[string]string{":message-type": "event", ":event-type": "messageStop"},
			[]byte(`{"stopReason":"end_turn"}`)),
		bedrockMetadata,
	})

	var got []string
	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "ask", Head: bedrockHead()}, func(d string) { got = append(got, d) })
	if err != nil {
		t.Fatal(err)
	}

	if resp.Output != "Converse streams." {
		t.Errorf("Output = %q, want %q", resp.Output, "Converse streams.")
	}
	if strings.Join(got, "") != resp.Output {
		t.Errorf("deltas %q do not reassemble to Output %q", got, resp.Output)
	}
	if resp.InputTokens != 44 || resp.OutputTokens != 13 {
		t.Errorf("counts = %d/%d, want 44/13 from the metadata frame", resp.InputTokens, resp.OutputTokens)
	}
	if resp.TokensEstimated {
		t.Error("TokensEstimated = true, but the metadata frame reported both counts")
	}
	if resp.TTFT <= 0 {
		t.Error("TTFT = 0, want the measured time to the first delta")
	}

	wantPath := "/model/anthropic.claude-3-5-sonnet-20241022-v2:0/converse-stream"
	if len(stub.paths) != 1 || stub.paths[0] != wantPath {
		t.Errorf("paths = %v, want %q", stub.paths, wantPath)
	}
	if !strings.HasPrefix(stub.header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
		t.Errorf("Authorization = %q, want a SigV4 signature", stub.header.Get("Authorization"))
	}
	if got := stub.header.Get("Accept"); got != "application/vnd.amazon.eventstream" {
		t.Errorf("Accept = %q, want the event stream media type", got)
	}
}

func TestBedrockStream_NoMetadataIsEstimatedAndLabelled(t *testing.T) {
	newBedrockStub(t, [][]byte{bedrockDelta("an answer with no metadata frame")})

	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "ask", Head: bedrockHead()}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.TokensEstimated {
		t.Error("TokensEstimated = false, so a call with no metadata would log as free")
	}
	if resp.InputTokens == 0 || resp.OutputTokens == 0 {
		t.Errorf("counts = %d/%d, want estimates rather than zero", resp.InputTokens, resp.OutputTokens)
	}
}

func TestBedrockStream_ExceptionFrameFailsTheDispatch(t *testing.T) {
	newBedrockStub(t, [][]byte{
		bedrockDelta("this much arrived, then "),
		encodeEventFrame(
			map[string]string{":message-type": "exception", ":exception-type": "throttlingException"},
			[]byte(`{"message":"Too many requests"}`)),
	})

	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "ask", Head: bedrockHead()}, func(string) {})
	if err == nil {
		t.Fatalf("an exception frame returned a partial as the answer: %q", resp.Output)
	}
	if !strings.Contains(err.Error(), "throttlingException") || !strings.Contains(err.Error(), "Too many requests") {
		t.Errorf("error = %v, want the exception type and its message", err)
	}
}

// toolUse and reasoningContent ride the same event as answer text.
func TestBedrockStream_ToolUseAndReasoningAreNotTheAnswer(t *testing.T) {
	newBedrockStub(t, [][]byte{
		encodeEventFrame(map[string]string{":message-type": "event", ":event-type": "contentBlockDelta"},
			[]byte(`{"contentBlockIndex":0,"delta":{"reasoningContent":{"text":"thinking out loud"}}}`)),
		encodeEventFrame(map[string]string{":message-type": "event", ":event-type": "contentBlockDelta"},
			[]byte(`{"contentBlockIndex":1,"delta":{"toolUse":{"input":"{\"q\":1}"}}}`)),
		bedrockDelta("42"),
		bedrockMetadata,
	})

	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "ask", Head: bedrockHead()}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Output != "42" {
		t.Errorf("Output = %q, want only the text delta", resp.Output)
	}
}
