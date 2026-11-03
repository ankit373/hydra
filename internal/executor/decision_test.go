// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/decision"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

// billingRequest is the documented support-ticket example, asked once in every dialect.
var billingRequest = decision.Request{
	Input: "I was charged twice for my subscription this month. Please refund the extra charge.",
	Questions: []decision.Question{
		{
			Kind:         decision.Predicate,
			Instructions: "Is this message about billing?",
		},
		{
			Kind:         decision.Choice,
			Instructions: "What is the tone of this message?",
			Options: []decision.Option{
				{Name: "angry", Description: "An upset or hostile message"},
				{Name: "calm", Description: "A neutral or polite message"},
				{Name: "excited", Description: "An enthusiastic or eager message"},
			},
		},
		{
			Kind:         decision.Score,
			Instructions: "How urgent is this message?",
			Options: []decision.Option{
				{Name: "Can wait"},
				{Name: "Needs attention this week"},
				{Name: "Needs attention today"},
			},
		},
	},
}

// billingProbabilities is what every fixture answers, per question.
var billingProbabilities = [][]float64{
	{0.98, 0.02},
	{0.15, 0.8, 0.05},
	{0.1, 0.1, 0.8},
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "decision", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// sameJSON compares two bodies as values, so key order and spacing do not matter.
func sameJSON(t *testing.T, got, want []byte) bool {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("sent body is not JSON: %v", err)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("fixture is not JSON: %v", err)
	}
	return reflect.DeepEqual(g, w)
}

// closeTo compares distributions to within float rounding, since 1 - P(true) is computed.
func closeTo(got, want []float64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if math.Abs(got[i]-want[i]) > 1e-12 {
			return false
		}
	}
	return true
}

// recordedServer checks each request against a recorded body and replies with a recorded answer.
func recordedServer(t *testing.T, path, request, response string) (*httptest.Server, *http.Header) {
	t.Helper()
	var seen http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.URL.Path != path:
			t.Errorf("path = %s, want %s", r.URL.Path, path)
		case !sameJSON(t, body, fixture(t, request)):
			t.Errorf("sent body differs from %s:\n%s", request, body)
		}
		_, _ = w.Write(fixture(t, response))
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestDecisionExecutor_EachDialectRoundTripsItsFixture(t *testing.T) {
	cases := []struct {
		name     string
		head     provider.Head
		path     string
		wantAuth string
		tokens   int
	}{
		{
			name: "typesafe",
			head: provider.Head{
				ID:       "decision/typesafe",
				Provider: "typesafe",
				Meta:     map[string]string{"model": "jev-latest"},
			},
			path:     "/v1/systemone",
			wantAuth: "Bearer ts-test",
			tokens:   120,
		},
		{
			name: "openai",
			head: provider.Head{
				ID:       "decision/openai",
				Provider: "openai",
				Meta:     map[string]string{"model": "gpt-6-luna"},
			},
			path:     "/v1/decisions",
			wantAuth: "Bearer oa-test",
			tokens:   42,
		},
		{
			name: "sglang",
			head: provider.Head{
				ID:        "decision/sglang/Cloudflare/clef",
				Provider:  "sglang",
				LocalOnly: true,
				Meta:      map[string]string{"model": "Cloudflare/clef"},
			},
			path:   "/v1/systemone",
			tokens: 96,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			testutil.NewSandbox(t)
			t.Setenv("TYPESAFE_API_KEY", "ts-test")
			t.Setenv("OPENAI_API_KEY", "oa-test")
			srv, seen := recordedServer(t, c.path, c.name+"_request.json", c.name+"_response.json")
			c.head.Endpoint = srv.URL

			res, err := DecisionExecutor{}.Execute(context.Background(), c.head, billingRequest)
			if err != nil {
				t.Fatal(err)
			}
			if got := seen.Get("Authorization"); got != c.wantAuth {
				t.Errorf("Authorization = %q, want %q", got, c.wantAuth)
			}
			for i, want := range billingProbabilities {
				if got := res.Answers[i].Probabilities; !closeTo(got, want) {
					t.Errorf("question %d probabilities = %v, want %v", i, got, want)
				}
			}
			if res.InputTokens != c.tokens {
				t.Errorf("InputTokens = %d, want the billed %d", res.InputTokens, c.tokens)
			}
		})
	}
}

// A local decision model is found by its info endpoint and answered with no key
// and no address but its own, so nothing leaves the machine.
func TestDecisionHeads_LocalModelRoutesWithNoKeyAndNoEgress(t *testing.T) {
	testutil.NewSandbox(t)
	var hosts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hosts = append(hosts, r.Host)
		if r.Header.Get("Authorization") != "" {
			t.Errorf("%s carried a credential to a local server", r.URL.Path)
		}
		switch r.URL.Path {
		case "/get_model_info":
			_, _ = w.Write(fixture(t, "sglang_model_info.json"))
		case "/v1/systemone":
			_, _ = w.Write(fixture(t, "sglang_response.json"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv(sglangHostEnv, srv.URL)

	heads := DecisionHeads(context.Background())
	if len(heads) != 1 || !heads[0].LocalOnly {
		t.Fatalf("heads = %+v, want only the local server, since no key is set", heads)
	}
	if _, err := (DecisionExecutor{}).Execute(context.Background(), heads[0], billingRequest); err != nil {
		t.Fatal(err)
	}
	local := strings.TrimPrefix(srv.URL, "http://")
	for _, h := range hosts {
		if h != local {
			t.Errorf("a request went to %s, not the local server", h)
		}
	}
}

// Something listening on the port that does not answer as SGLang must not become a head.
func TestDecisionHeads_PortAloneIsNotAnIdentity(t *testing.T) {
	testutil.NewSandbox(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()
	t.Setenv(sglangHostEnv, srv.URL)

	if heads := DecisionHeads(context.Background()); len(heads) != 0 {
		t.Errorf("heads = %+v, want none", heads)
	}
}

func TestDecisionHeads_HostedSourcesFollowTheirKeys(t *testing.T) {
	testutil.NewSandbox(t)
	t.Setenv("TYPESAFE_API_KEY", "ts-test")
	t.Setenv("TYPESAFE_DEFAULT_MODEL", "jev-preview")

	heads := DecisionHeads(context.Background())
	if len(heads) != 1 || heads[0].ID != "decision/typesafe" {
		t.Fatalf("heads = %+v, want only decision/typesafe", heads)
	}
	if got := heads[0].Meta["model"]; got != "jev-preview" {
		t.Errorf("model = %q, want the one TYPESAFE_DEFAULT_MODEL names", got)
	}
}

// A malformed distribution is a broken head: refused with a reason, never normalised.
func TestDecisionExecutor_RefusesABadDistribution(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "short of one",
			body: `{"model":"m","answers":{"0":{"type":"choice","confidence":0.5,` +
				`"probabilities":{"yes":0.5,"no":0.4}}},"usage":{"input_tokens":1,"output_tokens":0}}`,
			want: "probabilities sum to 0.9, not 1",
		},
		{
			name: "negative",
			body: `{"model":"m","answers":{"0":{"type":"choice","confidence":0.5,` +
				`"probabilities":{"yes":1.2,"no":-0.2}}},"usage":{"input_tokens":1,"output_tokens":0}}`,
			want: "outside [0, 1]",
		},
		{
			name: "an option nobody asked about",
			body: `{"model":"m","answers":{"0":{"type":"choice","confidence":0.5,` +
				`"probabilities":{"yes":0.5,"maybe":0.5}}},"usage":{"input_tokens":1,"output_tokens":0}}`,
			want: `gave no probability for "no"`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			testutil.NewSandbox(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			d := Decider{Head: provider.Head{ID: "decision/sglang/m", Provider: "sglang", Endpoint: srv.URL}}

			_, err := d.Decide(context.Background(), "Is the build green?", []string{"yes", "no"})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want one saying %q", err, c.want)
			}
		})
	}
}

// The verifier's Decide shape is one choice question whose answer comes back in option order.
func TestDecider_AnswersInOptionOrder(t *testing.T) {
	testutil.NewSandbox(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"model":"m","answers":{"0":{"type":"choice","confidence":0.8,` +
			`"probabilities":{"false":0.1,"true":0.9}}},"usage":{"input_tokens":3,"output_tokens":0}}`))
	}))
	defer srv.Close()
	d := Decider{Head: provider.Head{ID: "decision/sglang/m", Provider: "sglang", Endpoint: srv.URL}}

	got, err := d.Decide(context.Background(), "Is 7 a prime?", []string{"true", "false"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []float64{0.9, 0.1}; !reflect.DeepEqual(got, want) {
		t.Errorf("Decide = %v, want %v", got, want)
	}
}

func TestDecisionExecutor_RefusesARequestNoDialectAccepts(t *testing.T) {
	q := decision.Question{Kind: decision.Choice, Options: []decision.Option{{Name: "only"}}}
	head := provider.Head{ID: "decision/sglang/m", Provider: "sglang", Endpoint: "http://127.0.0.1:1"}
	req := decision.Request{Questions: []decision.Question{q}}

	_, err := DecisionExecutor{}.Execute(context.Background(), head, req)
	if err == nil || !strings.Contains(err.Error(), "takes 2 to 255 options, got 1") {
		t.Errorf("err = %v, want the option count refused before anything is sent", err)
	}
}
