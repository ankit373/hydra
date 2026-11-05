// SPDX-License-Identifier: MIT

package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postWith(t *testing.T, r Router, header map[string]string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	for k, v := range header {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	Handler(r, nil, "").ServeHTTP(w, req)
	return w
}

func withMetadata(class string) string {
	return `{"model":"hydra","metadata":{"task_class":"` + class + `"},"messages":[{"role":"user","content":"hi"}]}`
}

func TestExplicitClass_HeaderAliasMetadata(t *testing.T) {
	const want = "classification.banking77"
	for _, tc := range []struct {
		name   string
		header map[string]string
		body   string
		class  string
		status int
	}{
		{"none", nil, oneTurn, "", http.StatusOK},
		{"header", map[string]string{TaskClassHeader: "Classification.Banking77"}, oneTurn, want, http.StatusOK},
		{"alias", map[string]string{DomainHeader: want}, oneTurn, want, http.StatusOK},
		{"metadata", nil, withMetadata(want), want, http.StatusOK},
		{"header and metadata agree", map[string]string{TaskClassHeader: want}, withMetadata("CLASSIFICATION.banking77"), want, http.StatusOK},
		{"header and alias disagree", map[string]string{TaskClassHeader: want, DomainHeader: "classification.clinc150"}, oneTurn, "", http.StatusBadRequest},
		{"header and metadata disagree", map[string]string{TaskClassHeader: want}, withMetadata("classification.clinc150"), "", http.StatusBadRequest},
		{"no dot", map[string]string{TaskClassHeader: "go"}, oneTurn, "", http.StatusBadRequest},
		{"slash", nil, withMetadata("classify/banking77"), "", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &stubRouter{answer: Answer{Output: "ok"}}
			w := postWith(t, s, tc.header, tc.body)
			if w.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
			if tc.status == http.StatusOK && s.got.TaskClass != tc.class {
				t.Errorf("router was asked for class %q, want %q", s.got.TaskClass, tc.class)
			}
		})
	}
}

// A refusal has to name both values, or the client cannot tell which of its
// two spellings to drop.
func TestExplicitClass_DisagreementNamesBoth(t *testing.T) {
	w := postWith(t, &stubRouter{}, map[string]string{TaskClassHeader: "a.one"}, withMetadata("a.two"))
	if msg := w.Body.String(); !strings.Contains(msg, "a.one") || !strings.Contains(msg, "a.two") {
		t.Errorf("the refusal does not name both classes: %s", msg)
	}
}

func TestChat_EchoesTheClassAndItsBasis(t *testing.T) {
	s := &stubRouter{answer: Answer{Output: "ok", Evidence: &Evidence{TaskClass: "a.b", TaskClassBasis: "route"}}}
	w := postWith(t, s, nil, oneTurn)
	if got := w.Header().Get(TaskClassHeader); got != "a.b" {
		t.Errorf("%s = %q, want a.b", TaskClassHeader, got)
	}
	var body struct {
		Hydra Evidence `json:"hydra"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Hydra != (Evidence{TaskClass: "a.b", TaskClassBasis: "route"}) {
		t.Errorf("body hydra = %+v, want the router's evidence", body.Hydra)
	}

	// A router that reports nothing gets nothing invented for it.
	w = postWith(t, &stubRouter{answer: Answer{Output: "ok"}}, nil, oneTurn)
	if w.Header().Get(TaskClassHeader) != "" || strings.Contains(w.Body.String(), `"hydra":`) {
		t.Errorf("evidence written for a router that reported none: %s", w.Body.String())
	}
}

func TestChat_TemperatureRange(t *testing.T) {
	for _, tc := range []struct {
		temp   string // the raw JSON value, empty for absent
		status int
	}{
		{"", http.StatusOK},
		{"0", http.StatusOK},
		{"2", http.StatusOK},
		{"0.7", http.StatusOK},
		{"-0.1", http.StatusBadRequest},
		{"2.1", http.StatusBadRequest},
		{`"NaN"`, http.StatusBadRequest},
	} {
		body := oneTurn
		if tc.temp != "" {
			body = `{"model":"hydra","temperature":` + tc.temp + `,"messages":[{"role":"user","content":"hi"}]}`
		}
		s := &stubRouter{}
		w := postWith(t, s, nil, body)
		if w.Code != tc.status {
			t.Errorf("temperature %s: status %d, want %d", tc.temp, w.Code, tc.status)
			continue
		}
		if tc.status != http.StatusOK {
			continue
		}
		got := s.got.Temperature
		if tc.temp == "" && got != nil {
			t.Errorf("no temperature sent, router asked for %v", *got)
		}
		if tc.temp != "" && (got == nil || formatFloat(*got) != tc.temp) {
			t.Errorf("temperature %s did not reach the router: %v", tc.temp, got)
		}
	}
}

func formatFloat(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func TestChat_MaxCostHeader(t *testing.T) {
	for _, tc := range []struct {
		header string
		want   float64
		status int
	}{
		{"", 0, http.StatusOK},
		{"0.25", 0.25, http.StatusOK},
		{"-1", 0, http.StatusBadRequest},
		{"NaN", 0, http.StatusBadRequest},
		{"Inf", 0, http.StatusBadRequest},
		{"abc", 0, http.StatusBadRequest},
	} {
		h := map[string]string{}
		if tc.header != "" {
			h[MaxCostHeader] = tc.header
		}
		s := &stubRouter{}
		w := postWith(t, s, h, oneTurn)
		if w.Code != tc.status {
			t.Errorf("%s %q: status %d, want %d", MaxCostHeader, tc.header, w.Code, tc.status)
			continue
		}
		if tc.status == http.StatusOK && s.got.MaxCostUSD != tc.want {
			t.Errorf("%s %q reached the router as %v, want %v", MaxCostHeader, tc.header, s.got.MaxCostUSD, tc.want)
		}
	}
}
