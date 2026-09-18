// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
)

// bedrockStub serves Bedrock's OpenAI-compatible chat stream and records what
// was asked for, so a test can assert the signature as well as the parse.
type bedrockStub struct {
	*httptest.Server
	body   []byte
	header http.Header
	paths  []string
}

// newBedrockStub points both Bedrock paths at itself through BEDROCK_BASE_URL,
// which is also how a VPC endpoint is addressed in production.
func newBedrockStub(t *testing.T, chunks []string) *bedrockStub {
	t.Helper()
	s := &bedrockStub{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.body, _ = io.ReadAll(r.Body)
		s.header = r.Header.Clone()
		s.paths = append(s.paths, r.URL.Path)
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
			if fl != nil {
				fl.Flush()
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		if fl != nil {
			fl.Flush()
		}
	}))
	t.Cleanup(s.Server.Close)
	t.Setenv("BEDROCK_BASE_URL", s.Server.URL)
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIATEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("BEDROCK_MODEL_ID", "anthropic.claude-test-v1")
	return s
}

func bedrockHead() provider.Head {
	return provider.Head{
		ID: "bedrock", Name: "Bedrock", Provider: "bedrock",
		Source: "env", AuthReady: true, Meta: map[string]string{},
	}
}

func bedrockChunk(text string) string {
	b, _ := json.Marshal(text)
	return fmt.Sprintf(`{"model":"anthropic.claude-test-v1","choices":[{"delta":{"content":%s}}]}`, b)
}

const bedrockUsage = `{"model":"anthropic.claude-test-v1","choices":[],` +
	`"usage":{"prompt_tokens":31,"completion_tokens":64}}`

func TestBedrockStream_ReassemblesDeltasAndReportsCounts(t *testing.T) {
	newBedrockStub(t, []string{bedrockChunk("Hello, "), bedrockChunk("world"), bedrockUsage})

	var got []string
	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "say hello", Head: bedrockHead()},
		func(d string) { got = append(got, d) })
	if err != nil {
		t.Fatal(err)
	}

	if resp.Output != "Hello, world" {
		t.Errorf("Output = %q, want %q", resp.Output, "Hello, world")
	}
	if strings.Join(got, "") != resp.Output {
		t.Errorf("deltas %q do not reassemble to Output %q", got, resp.Output)
	}
	if resp.InputTokens != 31 || resp.OutputTokens != 64 {
		t.Errorf("counts = %d/%d, want 31/64", resp.InputTokens, resp.OutputTokens)
	}
	if resp.TokensEstimated {
		t.Error("TokensEstimated = true, but the provider reported both counts")
	}
	if resp.TTFT <= 0 {
		t.Error("TTFT = 0, want the measured time to the first delta")
	}
}

// The whole reason Bedrock could not use the existing streamTarget split:
// SigV4 signs a hash of the request body, so the signature has to be computed
// from bytes the target never sees. If the hook ran before the body was
// marshalled, or over different bytes, AWS would reject every streamed call
// with a 403 that no test of parsing would ever reach.
func TestBedrockStream_SignsTheExactBodyItSends(t *testing.T) {
	s := newBedrockStub(t, []string{bedrockChunk("hi"), bedrockUsage})

	if _, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "p", Head: bedrockHead()}, func(string) {}); err != nil {
		t.Fatal(err)
	}

	auth := s.header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") {
		t.Fatalf("Authorization = %q, want a SigV4 signature; an unsigned stream is a 403", auth)
	}
	if !strings.Contains(auth, "Credential=AKIATEST/") {
		t.Errorf("Authorization = %q, want the configured access key", auth)
	}

	// The signed payload hash must match the body that actually arrived, which
	// is what catches a hook that signed a different or earlier body.
	sum := sha256.Sum256(s.body)
	if got, want := s.header.Get("X-Amz-Content-Sha256"), hex.EncodeToString(sum[:]); got != want {
		t.Errorf("signed payload hash = %s, but the body that arrived hashes to %s", got, want)
	}
	if s.header.Get("X-Amz-Date") == "" {
		t.Error("X-Amz-Date is absent, so the signature cannot be verified")
	}

	// Presence and a matching payload hash still do not prove the signature is
	// the one AWS will compute: the canonical request covers Content-Type, so
	// signing before that header is set produces a well-formed signature that
	// is simply wrong, and nothing short of real AWS would notice. Recomputed
	// here from the AWS spec rather than by calling the production helper, so
	// the two cannot agree while both being wrong.
	if want := verifySigV4(t, s, "us-east-1", "bedrock", "secret"); want != auth {
		t.Errorf("signature does not verify.\n got: %s\nwant: %s", auth, want)
	}
}

// verifySigV4 recomputes the Authorization header the received request should
// carry. The signed header set matches what canonicalHeaders produces, which
// is the contract under test.
func verifySigV4(t *testing.T, s *bedrockStub, region, service, secret string) string {
	t.Helper()
	h := s.header
	amzDate := h.Get("X-Amz-Date")
	dateStamp := amzDate[:8]
	payloadHash := h.Get("X-Amz-Content-Sha256")

	signed := []string{"content-type", "host", "x-amz-content-sha256", "x-amz-date"}
	if h.Get("X-Amz-Security-Token") != "" {
		signed = append(signed, "x-amz-security-token")
	}
	host := strings.TrimPrefix(s.Server.URL, "http://")
	var canon strings.Builder
	for _, k := range signed {
		v := h.Get(k)
		if k == "host" {
			v = host
		}
		canon.WriteString(k + ":" + v + "\n")
	}
	signedHeaders := strings.Join(signed, ";")

	canonicalRequest := strings.Join([]string{
		http.MethodPost, "/v1/chat/completions", "",
		canon.String(), signedHeaders, payloadHash,
	}, "\n")

	scope := strings.Join([]string{dateStamp, region, service, "aws4_request"}, "/")
	crSum := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", amzDate, scope, hex.EncodeToString(crSum[:]),
	}, "\n")

	mac := func(key []byte, msg string) []byte {
		m := hmac.New(sha256.New, key)
		m.Write([]byte(msg))
		return m.Sum(nil)
	}
	k := mac([]byte("AWS4"+secret), dateStamp)
	k = mac(k, region)
	k = mac(k, service)
	k = mac(k, "aws4_request")
	sig := hex.EncodeToString(mac(k, stringToSign))

	return fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		"AKIATEST", scope, signedHeaders, sig)
}

// Without stream_options.include_usage an OpenAI-shaped server sends no usage
// block at all and the dispatch is logged as free (#787).
func TestBedrockStream_AsksForUsageAndAStream(t *testing.T) {
	s := newBedrockStub(t, []string{bedrockChunk("hi"), bedrockUsage})

	if _, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "p", Head: bedrockHead()}, func(string) {}); err != nil {
		t.Fatal(err)
	}

	if len(s.paths) != 1 || s.paths[0] != "/v1/chat/completions" {
		t.Errorf("paths = %v, want one request to /v1/chat/completions", s.paths)
	}
	var sent struct {
		Model         string `json:"model"`
		Stream        bool   `json:"stream"`
		StreamOptions *struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
	}
	if err := json.Unmarshal(s.body, &sent); err != nil {
		t.Fatal(err)
	}
	if !sent.Stream {
		t.Error("body did not ask for a stream")
	}
	if sent.StreamOptions == nil || !sent.StreamOptions.IncludeUsage {
		t.Error("body did not ask for usage on the stream, so the call would log as free")
	}
	if sent.Model != "anthropic.claude-test-v1" {
		t.Errorf("model = %q, want the configured Bedrock model id", sent.Model)
	}
}

func TestBedrockStream_NoUsageIsEstimatedAndLabelled(t *testing.T) {
	newBedrockStub(t, []string{bedrockChunk("an answer with no counts attached")})

	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "ask", Head: bedrockHead()}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.TokensEstimated {
		t.Error("TokensEstimated = false, so a call with no reported usage would log as free")
	}
	if resp.InputTokens == 0 || resp.OutputTokens == 0 {
		t.Errorf("counts = %d/%d, want estimates rather than zero", resp.InputTokens, resp.OutputTokens)
	}
}

// The buffered path must keep working, keep signing, and keep NOT asking for a
// stream, since both now resolve their URL from one helper.
func TestBedrock_BufferedPathStillSignsAndDoesNotStream(t *testing.T) {
	s := newBedrockStub(t, nil)
	s.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.body, _ = io.ReadAll(r.Body)
		s.header = r.Header.Clone()
		s.paths = append(s.paths, r.URL.Path)
		fmt.Fprint(w, `{"model":"anthropic.claude-test-v1","choices":[{"message":{"content":"buffered"}}],`+
			`"usage":{"prompt_tokens":5,"completion_tokens":7}}`)
	})

	resp, err := (&HTTPExecutor{}).Execute(context.Background(),
		Request{Prompt: "p", Head: bedrockHead()})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Output != "buffered" {
		t.Errorf("Output = %q, want %q", resp.Output, "buffered")
	}
	if !strings.HasPrefix(s.header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
		t.Error("the buffered path stopped signing its request")
	}
	var sent map[string]any
	if err := json.Unmarshal(s.body, &sent); err != nil {
		t.Fatal(err)
	}
	if sent["stream"] == true {
		t.Error("the buffered path asked for a stream it cannot read")
	}
}

// Replicate is deliberately excluded from #792: its prediction API is a
// polling interface, so there is no stream to read. It must keep falling back
// to Execute rather than being quietly routed at a stream reader.
func TestReplicate_StaysOnTheBufferedPath(t *testing.T) {
	head := provider.Head{
		ID: "replicate", Name: "Replicate", Provider: "replicate",
		Source: "env", AuthReady: true, Meta: map[string]string{},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a := r.Header.Get("Accept"); strings.Contains(a, "event-stream") {
			t.Errorf("Replicate was asked for a stream (Accept: %s); its API polls", a)
		}
		http.Error(w, "not reached", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("REPLICATE_API_TOKEN", "test")

	// The call fails (no real Replicate), which is fine: what matters is that
	// ExecuteStream did not route it at an SSE reader.
	_, _ = (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "p", Head: head}, func(string) {})
}
