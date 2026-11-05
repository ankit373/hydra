// SPDX-License-Identifier: MIT

package serve

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/rank"
)

func TestChat_ObjectiveHeader(t *testing.T) {
	for _, tc := range []struct {
		sent   string
		want   rank.Objective
		status int
	}{
		{"", "", http.StatusOK},
		{"quality", rank.Quality, http.StatusOK},
		{"COST", rank.Cost, http.StatusOK},
		{"balanced", "", http.StatusBadRequest},
	} {
		s := &stubRouter{answer: Answer{Output: "ok"}}
		var h http.Header
		if tc.sent != "" {
			h = http.Header{ObjectiveHeader: {tc.sent}}
		}
		w := postWith(t, s, h, oneTurn)
		if w.Code != tc.status {
			t.Fatalf("%q: status %d, want %d: %s", tc.sent, w.Code, tc.status, w.Body.String())
		}
		if tc.status == http.StatusBadRequest && !strings.Contains(w.Body.String(), "quality, cost") {
			t.Errorf("%q: the refusal does not list the accepted objectives: %s", tc.sent, w.Body.String())
		}
		if tc.status == http.StatusOK && s.got.Objective != tc.want {
			t.Errorf("%q: router was asked for objective %q, want %q", tc.sent, s.got.Objective, tc.want)
		}
	}
}

func TestChat_EchoesTheObjectiveAndBasis(t *testing.T) {
	ev := &Evidence{Objective: "quality", PPassBasis: "measured"}
	w := postWith(t, &stubRouter{answer: Answer{Output: "ok", Evidence: ev}}, nil, oneTurn)
	if w.Header().Get(ObjectiveHeader) != "quality" || w.Header().Get(BasisHeader) != "measured" {
		t.Errorf("headers %v, want the objective and basis echoed", w.Header())
	}
	// No class routed, so none is claimed, in the headers or the body.
	if _, ok := w.Header()[TaskClassHeader]; ok {
		t.Errorf("%s sent for a request that routed on no class", TaskClassHeader)
	}
	var body struct {
		Hydra map[string]string `json:"hydra"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Hydra) != 2 || body.Hydra["objective"] != "quality" || body.Hydra["p_pass_basis"] != "measured" {
		t.Errorf("body hydra = %v, want only the objective and basis", body.Hydra)
	}
}
