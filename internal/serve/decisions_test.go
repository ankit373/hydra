// SPDX-License-Identifier: MIT

package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/decide"
	"github.com/ankit373/hydra/internal/decision"
)

// stubDecider answers every question from a table keyed by its position, and records the call.
type stubDecider struct {
	answers []decision.Answer
	err     error
	got     decide.Call
}

func (s *stubDecider) Decide(_ context.Context, c decide.Call) (decide.Outcome, error) {
	s.got = c
	if s.err != nil {
		return decide.Outcome{}, s.err
	}
	res := decision.Result{Model: "jev-latest", Answers: s.answers, InputTokens: 120}
	return decide.Outcome{
		Result:     res,
		Asked:      []string{"decision/sglang/clef", "decision/typesafe"},
		Confidence: 0.97,
	}, nil
}

func postDecision(
	t *testing.T, d Decider, path, fixture string, header http.Header,
) *httptest.ResponseRecorder {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	for k, v := range header {
		req.Header[k] = v
	}
	w := httptest.NewRecorder()
	Handler(&stubRouter{}, d, "").ServeHTTP(w, req)
	return w
}

func TestSystemOne_AcceptsTheNativeBodyAndAnswersInItsShape(t *testing.T) {
	d := &stubDecider{answers: []decision.Answer{
		{Probabilities: []float64{0.98, 0.02}},
		{Probabilities: []float64{0.15, 0.8, 0.05}, Confidence: 0.7},
		{Probabilities: []float64{0.1, 0.1, 0.8}, Confidence: 0.55},
	}}
	header := http.Header{DomainHeader: {"support"}}
	w := postDecision(t, d, "/v1/systemone", "systemone_request.json", header)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if d.got.Domain != "support" || d.got.RunID == "" || d.got.RunID != w.Header().Get(RequestIDHeader) {
		t.Errorf("call = %+v, want the domain header and the request id passed through", d.got)
	}

	var got struct {
		Model   string `json:"model"`
		Answers map[string]struct {
			Type          string             `json:"type"`
			Noul          float64            `json:"noul"`
			Choice        string             `json:"choice"`
			Score         float64            `json:"score"`
			Legend        map[string]string  `json:"legend"`
			Probabilities map[string]float64 `json:"probabilities"`
		} `json:"answers"`
		Usage map[string]int `json:"usage"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if a := got.Answers["billing"]; a.Type != "noul" || a.Noul != 0.98 {
		t.Errorf("billing = %+v, want noul 0.98", a)
	}
	if a := got.Answers["tone"]; a.Choice != "calm" || a.Probabilities["angry"] != 0.15 {
		t.Errorf("tone = %+v, want calm with each option's probability", a)
	}
	urgency := got.Answers["urgency"]
	// The documented example: 0.1, 0.1, 0.8 over three levels scores 1.7.
	if math.Abs(urgency.Score-1.7) > 1e-9 || urgency.Legend["2"] != "Needs attention today" {
		t.Errorf("urgency = %+v, want score 1.7 with its legend", urgency)
	}
	if got.Usage["input_tokens"] != 120 {
		t.Errorf("usage = %v, want the billed input tokens", got.Usage)
	}
	if h := w.Header().Get(SourcesHeader); h != "decision/sglang/clef,decision/typesafe" {
		t.Errorf("%s = %q, want both sources in the order asked", SourcesHeader, h)
	}
	if h := w.Header().Get(ConfidenceHeader); h != "0.9700" {
		t.Errorf("%s = %q, want 0.9700", ConfidenceHeader, h)
	}
}

func TestDecisions_AcceptsTheNativeBodyAndAnswersInItsShape(t *testing.T) {
	d := &stubDecider{answers: []decision.Answer{
		{Probabilities: []float64{0.95, 0.02, 0.01, 0.02}, Confidence: 0.93},
		{Probabilities: []float64{0.1, 0.7, 0.2}, Confidence: 0.6},
		{Probabilities: []float64{0.9, 0.1}},
	}}
	w := postDecision(t, d, "/v1/decisions", "decisions_request.json", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	wantKinds := []decision.Kind{
		decision.Choice,
		decision.Score,
		decision.Predicate,
	}
	for i, q := range d.got.Request.Questions {
		if q.Kind != wantKinds[i] {
			t.Errorf("question %d parsed as %s, want %s", i, q.Kind, wantKinds[i])
		}
	}

	var got struct {
		Answers []struct {
			Type          string          `json:"type"`
			Name          *string         `json:"name"`
			Choice        json.RawMessage `json:"choice"`
			Score         float64         `json:"score"`
			Probability   float64         `json:"probability"`
			Probabilities []struct {
				Value       json.RawMessage `json:"value"`
				Label       string          `json:"label"`
				Probability float64         `json:"probability"`
			} `json:"probabilities"`
		} `json:"answers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Answers) != 3 {
		t.Fatalf("answers = %s, want three in question order", w.Body)
	}
	department, severity, refund := got.Answers[0], got.Answers[1], got.Answers[2]
	if string(department.Choice) != `"billing"` || *department.Name != "department" {
		t.Errorf("department = %+v, want billing under its own name", department)
	}
	// The documented example: 0.1, 0.7, 0.2 scores 1.1, levels keyed by index with their label.
	level := severity.Probabilities[2]
	if math.Abs(severity.Score-1.1) > 1e-9 || string(level.Value) != "2" || level.Label != "Fully blocked" {
		t.Errorf("severity = %+v, want score 1.1 and indexed levels", severity)
	}
	// An unnamed question is echoed with a null name, as the API does.
	if refund.Type != "predicate" || refund.Name != nil || refund.Probability != 0.9 {
		t.Errorf("refund = %+v, want an unnamed predicate at 0.9", refund)
	}
}

func TestDecisionEndpoints_RefuseWhatTheyCannotRoute(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		body   string
		status int
		want   string
	}{
		{
			name: "an image on System One",
			path: "/v1/systemone",
			body: `{"model":"m","state":"x","questions":{"q":{"type":"noul"}},` +
				`"assets":{"a":{"type":"image"}}}`,
			status: http.StatusBadRequest,
			want:   `"detail":"could not read the request: images are not routed yet`,
		},
		{
			name: "an image part on Decisions",
			path: "/v1/decisions",
			body: `{"model":"m",` +
				`"input":[{"role":"user","content":[{"type":"input_image","image_url":"https://x"}]}],` +
				`"questions":[{"type":"predicate","instructions":"x"}]}`,
			status: http.StatusBadRequest,
			want:   `"error":{"message":"could not read the request: input item 0: images are not routed yet`,
		},
		{
			name:   "a box question",
			path:   "/v1/systemone",
			body:   `{"model":"m","state":"x","questions":{"where":{"type":"box"}}}`,
			status: http.StatusBadRequest,
			want:   `question type \"box\" is not routed`,
		},
		{
			name: "one choice",
			path: "/v1/decisions",
			body: `{"model":"m","input":"x",` +
				`"questions":[{"type":"choice","instructions":"x","choices":[{"value":"a"}]}]}`,
			status: http.StatusBadRequest,
			want:   "takes 2 to 255 options, got 1",
		},
		{
			name:   "a GET",
			path:   "/v1/decisions",
			status: http.StatusMethodNotAllowed,
			want:   "use POST",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			method := http.MethodPost
			if c.body == "" {
				method = http.MethodGet
			}
			req := httptest.NewRequest(method, c.path, strings.NewReader(c.body))
			w := httptest.NewRecorder()
			Handler(&stubRouter{}, &stubDecider{}, "").ServeHTTP(w, req)
			if w.Code != c.status || !strings.Contains(w.Body.String(), c.want) {
				t.Errorf("got %d %s, want %d containing %s", w.Code, w.Body, c.status, c.want)
			}
		})
	}
}

// A routing failure is a gateway error in the client's own error shape.
func TestDecisionEndpoints_SourceFailureIsAGatewayError(t *testing.T) {
	d := &stubDecider{err: errors.New("no decision source answered: down")}
	w := postDecision(t, d, "/v1/systemone", "systemone_request.json", nil)
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"detail": "no decision source answered: down"}
	if w.Code != http.StatusBadGateway || !reflect.DeepEqual(got, want) {
		t.Errorf("got %d %v, want 502 %v", w.Code, got, want)
	}
}

func TestDecisionEndpoints_RequireTheToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/decisions", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	Handler(&stubRouter{}, &stubDecider{}, "secret").ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status %d without a token, want 401", w.Code)
	}
}
