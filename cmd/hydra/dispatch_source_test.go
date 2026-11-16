// SPDX-License-Identifier: MIT

package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/util"
)

var refundQuestion = "A customer asks how long they have to ask for a refund.\n\n" +
	util.WrapUntrusted("POLICY", "Refunds are given within 30 days of purchase, with the receipt.")

// supportHeads stands up two support heads, "a" cheaper and "b" stronger, each with a fixed reply.
func supportHeads(t *testing.T, first, second string) *cascadeFixture {
	t.Helper()
	testutil.NewSandbox(t)
	return litellmHeads(t, map[string]string{"a": first, "b": second}, "writing.support")
}

func TestDispatch_SourceOnlyNeedsAFence(t *testing.T) {
	f := supportHeads(t, "Refunds are given within 30 days.", "Refunds are given within 30 days.")
	_, _, err := run(t, "dispatch", "--no-stream", "--kind", "writing.support", "--source-only",
		"how long do I have to ask for a refund?")
	if err == nil || !strings.Contains(err.Error(), "--source-only needs the documents fenced") {
		t.Fatalf("err = %v, want a prompt with no fence refused", err)
	}
	if f.called("a")+f.called("b") != 0 {
		t.Errorf("a head ran on a refused request: a=%d b=%d", f.called("a"), f.called("b"))
	}
}

func TestDispatch_SourceOnlyRefusesUnsupportedReply(t *testing.T) {
	first, second := "You can ask for a refund within 90 days.", "You can ask for a refund within 60 days."
	f := supportHeads(t, first, second)
	out, _, err := run(t, "dispatch", "--no-stream", "--kind", "writing.support", "--source-only", refundQuestion)
	var status exitStatus
	if !errors.As(err, &status) || int(status) != groundUnsupported {
		t.Fatalf("err = %v, want exit %d", err, groundUnsupported)
	}
	if p := f.prompt("b"); !strings.Contains(p, sourceOnlyAsk) || !strings.Contains(p, "REJECTED ANSWER") {
		t.Errorf("want one rerun on b, with the source-only instruction:\n%s", p)
	}
	if !strings.Contains(out, "\n"+notCoveredReply+"\n") || !strings.Contains(out, "60 days") {
		t.Errorf("want the fixed reply with the failed sentence:\n%s", out)
	}
	for _, reply := range []string{first, second} {
		if strings.Contains(out, "\n"+reply+"\n") {
			t.Errorf("an unsupported reply was printed as the answer:\n%s", out)
		}
	}
}

func TestDispatch_SourceOnlyPassesSupportedReply(t *testing.T) {
	reply := "Refunds are given within 30 days of purchase."
	f := supportHeads(t, reply, "Refunds are given within 90 days.")
	out, cobraOut, err := run(t, "dispatch", "--no-stream", "--kind", "writing.support", "--source-only", refundQuestion)
	if err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	if !strings.Contains(out, "\n"+reply+"\n") || strings.Contains(out, notCoveredReply) || strings.Contains(f.prompt("b"), "REJECTED ANSWER") {
		t.Errorf("want the supported reply served as is, with no rerun:\n%s", out)
	}
}
