// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/decision"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

// pngData is the base64 body of the image greenRequest carries.
const pngData = "iVBORw0KGgo="

// greenRequest is one yes/no choice about an image, enough to exercise every transport choice.
var greenRequest = decision.Request{
	Input: "Is the build green?",
	Image: "data:image/png;base64," + pngData,
	Questions: []decision.Question{
		{
			Kind: decision.Choice,
			Options: []decision.Option{
				{Name: "yes"},
				{Name: "no"},
			},
		},
	},
}

// The replies each dialect gives greenRequest. The codecs are tested against
// documented fixtures in internal/decision; these only need to parse.
const (
	systemOneReply = `{"model":"m","answers":{"0":{"type":"choice","probabilities":{"yes":0.9,"no":0.1}}},` +
		`"usage":{"input_tokens":7,"output_tokens":0}}`
	decisionsReply = `{"model":"m","answers":[{"type":"choice","probabilities":[` +
		`{"value":"yes","probability":0.9},{"value":"no","probability":0.1}]}],"usage":{"input_tokens":7}}`
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "decision", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Each provider gets its own path, key, image header and image carrier.
func TestDecisionExecutor_EachProviderGetsItsTransport(t *testing.T) {
	cases := []struct {
		name        string
		head        provider.Head
		path        string
		wantAuth    string
		imageHeader string
		// carrier is the top-level field that must hold the image.
		carrier string
		reply   string
	}{
		{
			name: "typesafe",
			head: provider.Head{
				ID:       "decision/typesafe",
				Provider: "typesafe",
				Meta:     map[string]string{"model": "jev-latest"},
			},
			path:        "/v1/systemone",
			wantAuth:    "Bearer ts-test",
			imageHeader: "true",
			carrier:     "assets",
			reply:       systemOneReply,
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
			carrier:  "input",
			reply:    decisionsReply,
		},
		{
			name: "sglang",
			head: provider.Head{
				ID:        "decision/sglang/Cloudflare/clef",
				Provider:  "sglang",
				LocalOnly: true,
				Meta:      map[string]string{"model": "Cloudflare/clef"},
			},
			path:    "/v1/systemone",
			carrier: "images",
			reply:   systemOneReply,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			testutil.NewSandbox(t)
			t.Setenv("TYPESAFE_API_KEY", "ts-test")
			t.Setenv("OPENAI_API_KEY", "oa-test")
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != c.path {
					t.Errorf("path = %s, want %s", r.URL.Path, c.path)
				}
				if got := r.Header.Get("Authorization"); got != c.wantAuth {
					t.Errorf("Authorization = %q, want %q", got, c.wantAuth)
				}
				if got := r.Header.Get("TypeSafe-Danger-Zone-Images"); got != c.imageHeader {
					t.Errorf("image header = %q, want %q", got, c.imageHeader)
				}
				raw, _ := io.ReadAll(r.Body)
				var body map[string]json.RawMessage
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Errorf("sent body is not JSON: %v", err)
				}
				if !strings.Contains(string(body[c.carrier]), pngData) {
					t.Errorf("%s does not carry the image: %s", c.carrier, raw)
				}
				_, _ = w.Write([]byte(c.reply))
			}))
			defer srv.Close()
			c.head.Endpoint = srv.URL

			res, err := DecisionExecutor{}.Execute(context.Background(), c.head, greenRequest)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := res.Answers[0].Probabilities, []float64{0.9, 0.1}; !slices.Equal(got, want) {
				t.Errorf("probabilities = %v, want %v", got, want)
			}
			if res.InputTokens != 7 {
				t.Errorf("InputTokens = %d, want the billed 7", res.InputTokens)
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
			_, _ = w.Write([]byte(systemOneReply))
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
	if _, err := (DecisionExecutor{}).Execute(context.Background(), heads[0], greenRequest); err != nil {
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
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			testutil.NewSandbox(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			head := provider.Head{ID: "decision/sglang/m", Provider: "sglang", Endpoint: srv.URL}

			_, err := DecisionExecutor{}.Execute(context.Background(), head, greenRequest)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want one saying %q", err, c.want)
			}
		})
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
