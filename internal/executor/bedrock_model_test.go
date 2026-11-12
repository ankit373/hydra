// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"strings"
	"testing"
)

// A head naming its model must reach that model: Bedrock sent BEDROCK_MODEL_ID for
// every head, so two benchmark arms on two models were one model billed twice.
func TestBedrock_SendsTheHeadsModel(t *testing.T) {
	const converse = `{"output":{"message":{"content":[{"text":"ok"}]}},"stopReason":"end_turn","usage":{"inputTokens":3,"outputTokens":1}}`
	for _, c := range []struct{ meta, want string }{
		{"amazon.nova-micro-v1:0", "amazon.nova-micro-v1:0"},
		{"", "anthropic.claude-3-5-sonnet-20241022-v2:0"}, // the env default, as before
	} {
		_, got := bedrockBuffered(t, converse)
		h := bedrockHead()
		if c.meta != "" {
			h.Meta["model"] = c.meta
		}
		resp, err := (&HTTPExecutor{}).Execute(context.Background(), Request{Prompt: "hi", Head: h})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got.path, "/model/"+c.want+"/") || resp.Model != c.want {
			t.Errorf("meta %q: posted to %s, reported %q; want model %s", c.meta, got.path, resp.Model, c.want)
		}
	}

	s := newBedrockStub(t, [][]byte{bedrockDelta("ok"), bedrockMetadata})
	h := bedrockHead()
	h.Meta["model"] = "amazon.nova-micro-v1:0"
	if _, err := Stream(context.Background(), &HTTPExecutor{}, Request{Prompt: "hi", Head: h}, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if len(s.paths) != 1 || !strings.Contains(s.paths[0], "/model/amazon.nova-micro-v1:0/") {
		t.Errorf("stream posted to %v, want the head's model", s.paths)
	}
}
