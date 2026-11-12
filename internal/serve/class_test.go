// SPDX-License-Identifier: MIT

package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postWith(t *testing.T, r Router, header http.Header, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	w := httptest.NewRecorder()
	Handler(r, nil, "").ServeHTTP(w, req)
	return w
}

func TestExplicitClass_Header(t *testing.T) {
	const want = "classification.banking77"
	hdr := func(vs ...string) http.Header { return http.Header{TaskClassHeader: vs} }
	for _, tc := range []struct {
		name   string
		header http.Header
		class  string
		status int
	}{
		{"none", nil, "", http.StatusOK},
		{"header", hdr("Classification.Banking77"), want, http.StatusOK},
		{"repeated header agrees", hdr(want, "CLASSIFICATION.banking77"), want, http.StatusOK},
		{"repeated header disagrees", hdr(want, "classification.clinc150"), "", http.StatusBadRequest},
		{"no dot", hdr("go"), "", http.StatusBadRequest},
		{"slash", hdr("classify/banking77"), "", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &stubRouter{answer: Answer{Output: "ok"}}
			w := postWith(t, s, tc.header, oneTurn)
			if w.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
			if tc.status == http.StatusOK && s.got.TaskClass != tc.class {
				t.Errorf("router was asked for class %q, want %q", s.got.TaskClass, tc.class)
			}
		})
	}
}

// A refusal has to name both values, or the client cannot tell which to drop.
func TestExplicitClass_DisagreementNamesBoth(t *testing.T) {
	w := postWith(t, &stubRouter{}, http.Header{TaskClassHeader: {"a.one", "a.two"}}, oneTurn)
	if msg := w.Body.String(); w.Code != http.StatusBadRequest || !strings.Contains(msg, "a.one") || !strings.Contains(msg, "a.two") {
		t.Errorf("status %d, the refusal does not name both classes: %s", w.Code, msg)
	}
}

// Clients already send metadata of their own, and serve has never read it: at
// checkpoint A the class comes from the header alone, so metadata is inert.
func TestChat_MetadataIsInert(t *testing.T) {
	for _, md := range []string{
		`{"task_class":"support"}`,
		`{"task_class":"classification.banking77"}`,
		`{"tags":["a","b"],"user_id":42}`,
		`["not","an","object"]`,
	} {
		s := &stubRouter{answer: Answer{Output: "ok"}}
		w := postWith(t, s, nil, `{"model":"hydra","metadata":`+md+`,"messages":[{"role":"user","content":"hi"}]}`)
		if w.Code != http.StatusOK || s.got.TaskClass != "" {
			t.Errorf("metadata %s: status %d, class %q; want 200 and no class", md, w.Code, s.got.TaskClass)
		}
	}
}

// X-Hydra-Domain stays a /v1/decisions header until #1210's rename. Chat never
// read it, and a client still sending a bare domain like `support` must not
// start getting a 400.
func TestChat_IgnoresTheDecisionsDomainHeader(t *testing.T) {
	s := &stubRouter{answer: Answer{Output: "ok"}}
	w := postWith(t, s, http.Header{DomainHeader: {"support"}}, oneTurn)
	if w.Code != http.StatusOK || s.got.TaskClass != "" {
		t.Errorf("%s: support gave status %d, class %q; want 200 and no class", DomainHeader, w.Code, s.got.TaskClass)
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

// A stream's headers leave with its first frame, so the class has to be known
// by then. Result() is the header snapshot WriteHeader took, not the live map.
func TestStream_EchoesTheClassInItsHeaders(t *testing.T) {
	ev := &Evidence{TaskClass: "a.b", TaskClassBasis: "header"}
	announced := &streamRouter{events: []Event{
		{Kind: EventEvidence, Evidence: ev},
		{Kind: EventDelta, Text: "hel", Head: "h1"},
		{Kind: EventDelta, Text: "lo", Head: "h1"},
	}, answer: Answer{Output: "hello", Head: "h1"}}
	ignoring := &stubRouter{answer: Answer{Output: "hello", Evidence: ev}}

	for name, r := range map[string]Router{"announced": announced, "answer only": ignoring} {
		w := stream(t, r, streamTurn)
		if got := w.Result().Header.Get(TaskClassHeader); got != "a.b" {
			t.Errorf("%s: streamed %s = %q at the first flush, want a.b", name, TaskClassHeader, got)
		}
	}
}

// With the class header, the temperature is validated and forwarded.
func TestChat_TemperatureRange(t *testing.T) {
	for _, tc := range []struct {
		temp   string // the raw JSON value, empty for absent
		status int
	}{
		{"", http.StatusOK},
		{"null", http.StatusOK},
		{"0", http.StatusOK},
		{"2", http.StatusOK},
		{"0.7", http.StatusOK},
		{"-0.1", http.StatusBadRequest},
		{"2.1", http.StatusBadRequest},
		{"2.5", http.StatusBadRequest},
		{`"NaN"`, http.StatusBadRequest},
	} {
		body := oneTurn
		if tc.temp != "" {
			body = `{"model":"hydra","temperature":` + tc.temp + `,"messages":[{"role":"user","content":"hi"}]}`
		}
		s := &stubRouter{}
		w := postWith(t, s, http.Header{TaskClassHeader: {"a.b"}}, body)
		if w.Code != tc.status {
			t.Errorf("temperature %s: status %d, want %d", tc.temp, w.Code, tc.status)
			continue
		}
		if tc.status != http.StatusOK {
			continue
		}
		got := s.got.Temperature
		absent := tc.temp == "" || tc.temp == "null"
		if absent && got != nil {
			t.Errorf("no temperature sent, router asked for %v", *got)
		}
		if !absent && (got == nil || formatFloat(*got) != tc.temp) {
			t.Errorf("temperature %s did not reach the router: %v", tc.temp, got)
		}
	}
}

// G7d. Without the class header a client has not opted into E1, and serve
// ignores its temperature exactly as before: no 400, nothing forwarded.
func TestChat_TemperatureWithoutTheClassHeaderIsIgnored(t *testing.T) {
	for _, temp := range []string{"2.5", "0.7", `"NaN"`} {
		s := &stubRouter{answer: Answer{Output: "ok"}}
		w := postWith(t, s, nil, `{"model":"hydra","temperature":`+temp+`,"messages":[{"role":"user","content":"hi"}]}`)
		if w.Code != http.StatusOK || s.got.Temperature != nil {
			t.Errorf("temperature %s without %s: status %d, forwarded a temperature: %v; want 200 and nothing forwarded",
				temp, TaskClassHeader, w.Code, s.got.Temperature != nil)
		}
	}
}

func formatFloat(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func TestChat_MaxCostHeader(t *testing.T) {
	for _, tc := range []struct {
		values []string
		want   float64
		status int
	}{
		{nil, 0, http.StatusOK},
		{[]string{"0.25"}, 0.25, http.StatusOK},
		{[]string{"0.25", "0.25"}, 0.25, http.StatusOK},
		{[]string{"0.25", "0.5"}, 0, http.StatusBadRequest},
		{[]string{"0"}, 0, http.StatusBadRequest},
		{[]string{"-1"}, 0, http.StatusBadRequest},
		{[]string{"NaN"}, 0, http.StatusBadRequest},
		{[]string{"Inf"}, 0, http.StatusBadRequest},
		{[]string{"abc"}, 0, http.StatusBadRequest},
	} {
		s := &stubRouter{}
		w := postWith(t, s, http.Header{MaxCostHeader: tc.values}, oneTurn)
		if w.Code != tc.status {
			t.Errorf("%s %q: status %d, want %d", MaxCostHeader, tc.values, w.Code, tc.status)
			continue
		}
		if tc.status == http.StatusOK && s.got.MaxCostUSD != tc.want {
			t.Errorf("%s %q reached the router as %v, want %v", MaxCostHeader, tc.values, s.got.MaxCostUSD, tc.want)
		}
	}
}

// Two different ceilings on one request is ambiguity, and the refusal must
// say which two so the client can find the duplicate.
func TestChat_MaxCostDisagreementNamesBoth(t *testing.T) {
	w := postWith(t, &stubRouter{}, http.Header{MaxCostHeader: {"0.25", "0.5"}}, oneTurn)
	if msg := w.Body.String(); !strings.Contains(msg, "0.25") || !strings.Contains(msg, "0.5") {
		t.Errorf("the refusal does not name both ceilings: %s", msg)
	}
}
