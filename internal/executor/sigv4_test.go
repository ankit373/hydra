// SPDX-License-Identifier: MIT

package executor

import (
	"bytes"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

// AWS SigV4 request signing had 0% coverage. It is the highest-consequence code
// in this package: get it wrong and every Bedrock call fails with a 403 that
// says nothing useful, and the failure mode of getting it *subtly* wrong is
// worse, a signature that works today and breaks on a header change.
//
// Tested against known-answer vectors rather than by re-deriving the algorithm
// in the test, which would only prove the code agrees with itself. The vectors
// below were computed with an independent implementation (Python's hmac +
// hashlib) and the first matches the value AWS publishes in its own
// "deriving the signing key" documentation.

// AWS's published example: secret wJalrX…EXAMPLEKEY, 20120215/us-east-1/iam.
const (
	awsExampleSecret = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
	awsExampleKeyHex = "f4780e2d9f65fa895f9c67b32ce1baf0b0d8a43505a000a1a9e090d414db404d"
	// A second, independent vector so a single lucky match cannot pass.
	awsSecondKeyHex = "938127b5336810ddb6a5d6af445fcac9e371f9ed418ed386b022aed82901be75"
	// Well-known: SHA-256 of the empty string.
	emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

func TestAWSSigningKey_MatchesPublishedVectors(t *testing.T) {
	cases := []struct {
		name              string
		date, region, svc string
		want              string
	}{
		{"AWS documented example", "20120215", "us-east-1", "iam", awsExampleKeyHex},
		{"second vector", "20150830", "us-east-1", "service", awsSecondKeyHex},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hex.EncodeToString(awsSigningKey(awsExampleSecret, tc.date, tc.region, tc.svc))
			if got != tc.want {
				t.Errorf("awsSigningKey(%s/%s/%s)\n got  %s\n want %s",
					tc.date, tc.region, tc.svc, got, tc.want)
			}
		})
	}
}

func TestSHA256Hex_MatchesKnownAnswer(t *testing.T) {
	if got := sha256Hex(nil); got != emptySHA256 {
		t.Errorf("sha256Hex(nil) = %s, want %s", got, emptySHA256)
	}
	if got := sha256Hex([]byte{}); got != emptySHA256 {
		t.Errorf("sha256Hex(empty) = %s, want %s", got, emptySHA256)
	}
	// Different input must give a different digest, guards against a stub that
	// always returns the same thing.
	if sha256Hex([]byte("a")) == emptySHA256 {
		t.Error("sha256Hex is not actually hashing its input")
	}
}

// The signing key must depend on every input. A derivation that ignores one
// (say, region) produces signatures that validate in one region and fail in
// another, which is a nightmare to diagnose from a 403.
func TestAWSSigningKey_DependsOnEveryInput(t *testing.T) {
	base := awsSigningKey("secret", "20240101", "us-east-1", "bedrock")
	variants := map[string][]byte{
		"secret":  awsSigningKey("other", "20240101", "us-east-1", "bedrock"),
		"date":    awsSigningKey("secret", "20240102", "us-east-1", "bedrock"),
		"region":  awsSigningKey("secret", "20240101", "eu-west-1", "bedrock"),
		"service": awsSigningKey("secret", "20240101", "us-east-1", "s3"),
	}
	for what, got := range variants {
		if bytes.Equal(base, got) {
			t.Errorf("changing %s did not change the signing key, it is not part of the derivation", what)
		}
	}
}

func signedRequest(t *testing.T, body []byte, withToken bool) *http.Request {
	t.Helper()
	s := testutil.NewSandbox(t)
	s.SetKey(t, "AWS_ACCESS_KEY_ID", "AKIDEXAMPLE")
	s.SetKey(t, "AWS_SECRET_ACCESS_KEY", awsExampleSecret)
	if withToken {
		s.SetKey(t, "AWS_SESSION_TOKEN", "session-token-value")
	}

	req, err := http.NewRequest(http.MethodPost,
		"https://bedrock-runtime.us-east-1.amazonaws.com/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if err := signAWSRequest(req, body, "us-east-1", "bedrock"); err != nil {
		t.Fatalf("signAWSRequest: %v", err)
	}
	return req
}

// Every header named in SignedHeaders must actually be sent. AWS recomputes the
// canonical request from the headers it receives; a header declared but absent
// (or present but not declared) yields a different canonical request and a 403.
func TestSignAWSRequest_SignedHeadersMatchWhatIsSent(t *testing.T) {
	body := []byte(`{"model":"x"}`)
	req := signedRequest(t, body, false)

	auth := req.Header.Get("Authorization")
	if auth == "" {
		t.Fatal("no Authorization header was set")
	}
	signed := ""
	for _, part := range strings.Split(auth, ", ") {
		if strings.HasPrefix(part, "SignedHeaders=") {
			signed = strings.TrimPrefix(part, "SignedHeaders=")
		}
	}
	if signed == "" {
		t.Fatalf("no SignedHeaders in %q", auth)
	}

	for _, h := range strings.Split(signed, ";") {
		if h == "host" {
			continue // host comes from the URL, not the header map
		}
		if req.Header.Get(h) == "" {
			t.Errorf("SignedHeaders declares %q but the request does not send it, "+
				"AWS will compute a different canonical request and reject this with a 403", h)
		}
	}
}

// SignedHeaders must be lowercase and sorted; SigV4 requires it and AWS will
// not reorder them for us.
func TestSignAWSRequest_SignedHeadersAreLowercaseAndSorted(t *testing.T) {
	for _, withToken := range []bool{false, true} {
		req := signedRequest(t, []byte(`{}`), withToken)
		auth := req.Header.Get("Authorization")
		var signed string
		for _, part := range strings.Split(auth, ", ") {
			if strings.HasPrefix(part, "SignedHeaders=") {
				signed = strings.TrimPrefix(part, "SignedHeaders=")
			}
		}
		hs := strings.Split(signed, ";")
		for i, h := range hs {
			if h != strings.ToLower(h) {
				t.Errorf("token=%v: SignedHeaders entry %q is not lowercase", withToken, h)
			}
			if i > 0 && hs[i-1] >= h {
				t.Errorf("token=%v: SignedHeaders not sorted: %q before %q (full: %s)",
					withToken, hs[i-1], h, signed)
			}
		}
		// The session token must be signed when present and absent when not,
		// signing a header you do not send, or sending one you did not sign,
		// both produce a 403.
		hasTokenHeader := strings.Contains(signed, "x-amz-security-token")
		if hasTokenHeader != withToken {
			t.Errorf("withToken=%v but SignedHeaders %s x-amz-security-token",
				withToken, map[bool]string{true: "contains", false: "omits"}[hasTokenHeader])
		}
	}
}

// The payload hash must be the hash of the payload actually sent. Signing an
// empty body while sending a real one is a 403 that looks like a credentials
// problem.
func TestSignAWSRequest_PayloadHashCoversTheRealBody(t *testing.T) {
	body := []byte(`{"model":"claude","messages":[]}`)
	req := signedRequest(t, body, false)

	if got, want := req.Header.Get("X-Amz-Content-Sha256"), sha256Hex(body); got != want {
		t.Errorf("X-Amz-Content-Sha256 = %s, want %s (hash of the body being sent)", got, want)
	}
	// And a different body must produce a different hash, or the header is
	// carrying a constant.
	other := signedRequest(t, []byte(`{"different":true}`), false)
	if other.Header.Get("X-Amz-Content-Sha256") == req.Header.Get("X-Amz-Content-Sha256") {
		t.Error("two different bodies produced the same payload hash")
	}
}

// The secret must never leave the process. The Authorization header carries the
// access key ID and a derived signature, never the secret itself.
func TestSignAWSRequest_NeverLeaksTheSecret(t *testing.T) {
	req := signedRequest(t, []byte(`{"a":1}`), true)

	for name, values := range req.Header {
		for _, v := range values {
			if strings.Contains(v, awsExampleSecret) {
				t.Errorf("header %s leaks AWS_SECRET_ACCESS_KEY verbatim: %q", name, v)
			}
		}
	}
	auth := req.Header.Get("Authorization")
	if !strings.Contains(auth, "AKIDEXAMPLE") {
		t.Errorf("Authorization does not carry the access key ID: %q", auth)
	}
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") {
		t.Errorf("Authorization does not use the SigV4 scheme: %q", auth)
	}
}

// The credential scope must be date/region/service/aws4_request, in that order.
// AWS rejects any other shape outright.
func TestSignAWSRequest_CredentialScopeIsWellFormed(t *testing.T) {
	req := signedRequest(t, []byte(`{}`), false)
	auth := req.Header.Get("Authorization")

	var cred string
	for _, part := range strings.Split(auth, ", ") {
		if strings.HasPrefix(part, "AWS4-HMAC-SHA256 Credential=") {
			cred = strings.TrimPrefix(part, "AWS4-HMAC-SHA256 Credential=")
		}
	}
	if cred == "" {
		t.Fatalf("no Credential= in %q", auth)
	}
	parts := strings.Split(cred, "/")
	if len(parts) != 5 {
		t.Fatalf("Credential has %d segments, want 5 (keyID/date/region/service/aws4_request): %q",
			len(parts), cred)
	}
	if parts[0] != "AKIDEXAMPLE" {
		t.Errorf("credential key ID = %q", parts[0])
	}
	if parts[2] != "us-east-1" || parts[3] != "bedrock" {
		t.Errorf("credential region/service = %s/%s, want us-east-1/bedrock", parts[2], parts[3])
	}
	if parts[4] != "aws4_request" {
		t.Errorf("credential terminator = %q, want aws4_request", parts[4])
	}
	// The date segment must match X-Amz-Date's date part, or the scope and the
	// timestamp disagree and AWS rejects it.
	amzDate := req.Header.Get("X-Amz-Date")
	if len(amzDate) < 8 || parts[1] != amzDate[:8] {
		t.Errorf("credential date %q does not match X-Amz-Date %q", parts[1], amzDate)
	}
}

// Missing credentials must be an error, not an unsigned request sent anyway.
func TestSignAWSRequest_RefusesWithoutCredentials(t *testing.T) {
	cases := []struct {
		name       string
		id, secret string
	}{
		{"no credentials at all", "", ""},
		{"access key only", "AKID", ""},
		{"secret only", "", "shh"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testutil.NewSandbox(t)
			if tc.id != "" {
				s.SetKey(t, "AWS_ACCESS_KEY_ID", tc.id)
			}
			if tc.secret != "" {
				s.SetKey(t, "AWS_SECRET_ACCESS_KEY", tc.secret)
			}
			req, err := http.NewRequest(http.MethodPost, "https://example.com/x", nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := signAWSRequest(req, nil, "us-east-1", "bedrock"); err == nil {
				t.Error("signed a request with incomplete credentials instead of erroring")
			}
			if req.Header.Get("Authorization") != "" {
				t.Error("an Authorization header was set despite the failure")
			}
		})
	}
}

// canonicalHeaders builds half of the string-to-sign. Its output must end every
// line with \n and never contain a stray value, SigV4 is byte-exact.
func TestCanonicalHeaders_ShapeIsByteExact(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://host.example.com/p", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Amz-Date", "20240101T000000Z")
	req.Header.Set("X-Amz-Content-Sha256", emptySHA256)

	canonical, signed := canonicalHeaders(req)
	if !strings.HasSuffix(canonical, "\n") {
		t.Error("canonical headers block does not end with a newline")
	}
	for _, line := range strings.Split(strings.TrimSuffix(canonical, "\n"), "\n") {
		if !strings.Contains(line, ":") {
			t.Errorf("canonical header line %q has no colon", line)
		}
		if strings.HasSuffix(line, " ") || strings.Contains(line, ": ") {
			t.Errorf("canonical header line %q has untrimmed whitespace, SigV4 is byte-exact", line)
		}
	}
	// host must come from the URL, not from a Host header that Go does not
	// populate in Header.
	if !strings.Contains(canonical, "host:host.example.com\n") {
		t.Errorf("canonical headers missing host from the URL:\n%s", canonical)
	}
	// Every declared header must appear in the block.
	for _, h := range strings.Split(signed, ";") {
		if !strings.Contains(canonical, h+":") {
			t.Errorf("signed header %q is not in the canonical block", h)
		}
	}
}
