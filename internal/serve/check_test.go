// SPDX-License-Identifier: MIT

package serve

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestServe_UnknownCheckIs400(t *testing.T) {
	for _, c := range []string{"cmd:rm -rf {file}", "match:labels=/etc/passwd", "tests"} {
		s := &stubRouter{answer: Answer{Output: "ok"}}
		w := postWith(t, s, http.Header{CheckHeader: {c}}, oneTurn)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "match:pattern=") {
			t.Errorf("%q: status %d, want 400 naming the checks: %s", c, w.Code, w.Body.String())
		}
	}
}

// Each header value is one check: a pattern with a comma must not be split.
func TestServe_CheckHeaderReachesTheRouter(t *testing.T) {
	s := &stubRouter{answer: Answer{Output: "ok"}}
	w := postWith(t, s, http.Header{CheckHeader: {"ground", "match:pattern=a,b"}}, oneTurn)
	if w.Code != http.StatusOK || len(s.got.Checks) != 2 || s.got.Checks[1] != "match:pattern=a,b" {
		t.Errorf("status %d, router asked for %q", w.Code, s.got.Checks)
	}
}

func TestServe_CheckVerdictInHeadersAndBody(t *testing.T) {
	ev := &Evidence{CheckResult: CheckFailed, CheckDetail: "verifier:grounding: not in the context given: number 450"}
	w := postWith(t, &stubRouter{answer: Answer{Output: "450", Evidence: ev}}, http.Header{CheckHeader: {"ground"}}, oneTurn)
	if w.Header().Get(CheckResultHeader) != CheckFailed || w.Header().Get(CheckDetailHeader) != ev.CheckDetail {
		t.Errorf("headers %v, want the verdict", w.Header())
	}
	var body struct {
		Hydra Evidence `json:"hydra"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Hydra.CheckResult != CheckFailed {
		t.Errorf("body hydra = %+v (%v), want the verdict", body.Hydra, err)
	}
}

// A stream's headers leave with its first frame, so the verdict rides on the final chunk.
func TestServe_StreamCarriesVerdictInFinalChunk(t *testing.T) {
	r := &streamRouter{
		events: []Event{{Kind: EventDelta, Text: "The total is 120.", Head: "h"}},
		answer: Answer{Output: "The total is 120.", Head: "h",
			Evidence: &Evidence{CheckResult: CheckPassed, CheckDetail: "verifier:grounding"}},
	}
	fs, done := frames(t, stream(t, r, streamTurn).Body.String())
	if !done {
		t.Fatal("no [DONE]")
	}
	last := fs[len(fs)-1]
	hydra, _ := last["hydra"].(map[string]any)
	if choice(t, last)["finish_reason"] == nil || hydra["check_result"] != CheckPassed || hydra["check_detail"] != "verifier:grounding" {
		t.Errorf("final chunk %v, want the finish reason and the verdict under hydra", last)
	}
}
