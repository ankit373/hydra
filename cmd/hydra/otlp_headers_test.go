// SPDX-License-Identifier: MIT

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The variable is hand-typed, so a typo is the expected failure rather than an
// edge case. Dropping a malformed pair sends an unauthenticated request, and
// the 401 that comes back reads as the collector's fault, not the caller's.
func TestOTLPHeaders_RefusesAMalformedPair(t *testing.T) {
	for _, spec := range []string{
		"Authorization Basic abc123", // the space-for-equals typo
		"Authorization",              // key alone
		"=value",                     // no key
		"  =v",                       // no key once trimmed
		"Authorization=Basic abc,oops",
	} {
		t.Run(spec, func(t *testing.T) {
			got, err := otlpHeaders(spec)
			if err == nil {
				t.Fatalf("otlpHeaders(%q) = %v, want an error: a silently dropped "+
					"pair sends an unauthenticated request", spec, got)
			}
			if !strings.Contains(err.Error(), otlpHeadersEnv) {
				t.Errorf("error %q does not name %s, so a reader cannot tell what to fix",
					err, otlpHeadersEnv)
			}
		})
	}
}

func TestOTLPHeaders_ParsesWhatItShould(t *testing.T) {
	cases := map[string]struct {
		spec string
		want map[string]string
	}{
		"empty":              {"", nil},
		"blank":              {"   ", nil},
		"one":                {"Authorization=Basic abc123", map[string]string{"Authorization": "Basic abc123"}},
		"two":                {"Authorization=Basic abc,X-Scope=demo", map[string]string{"Authorization": "Basic abc", "X-Scope": "demo"}},
		"spaces are trimmed": {" Authorization = Basic abc ", map[string]string{"Authorization": "Basic abc"}},
		// A trailing comma says nothing either way, so it is tolerated rather
		// than refused; it is not a typo that costs anyone a request.
		"trailing comma": {"Authorization=Basic abc,", map[string]string{"Authorization": "Basic abc"}},
		// An empty value is legal per the OTEL spec this borrows its shape from.
		"empty value": {"X-Scope=", map[string]string{"X-Scope": ""}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := otlpHeaders(c.spec)
			if err != nil {
				t.Fatalf("otlpHeaders(%q) errored: %v", c.spec, err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("otlpHeaders(%q) = %v, want %v", c.spec, got, c.want)
			}
			for k, v := range c.want {
				if got[k] != v {
					t.Errorf("header %q = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

// A base64 Authorization header contains "=" padding, which must survive the
// k=v split or every Langfuse export fails to authenticate.
func TestOTLPHeaders_KeepsBase64PaddingInTheValue(t *testing.T) {
	got, err := otlpHeaders("Authorization=Basic cGs6c2s=")
	if err != nil {
		t.Fatal(err)
	}
	if want := "Basic cGs6c2s="; got["Authorization"] != want {
		t.Errorf("Authorization = %q, want %q: the value's own = was eaten by the split",
			got["Authorization"], want)
	}
}

// The header has to reach the wire. Parsing it correctly and not sending it is
// the same outcome as not parsing it.
func TestPostOTLP_AppliesHeadersAndContentType(t *testing.T) {
	var gotAuth, gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotType = r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv(otlpHeadersEnv, "Authorization=Basic cGs6c2s=")
	if err := postOTLP(srv.URL, []byte(`{"resourceSpans":[]}`), 0); err != nil {
		t.Fatalf("postOTLP: %v", err)
	}
	if gotAuth != "Basic cGs6c2s=" {
		t.Errorf("Authorization on the wire = %q, want the parsed value", gotAuth)
	}
	if gotType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotType)
	}
}

func TestPostOTLP_RefusesRatherThanSendingAMalformedHeader(t *testing.T) {
	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv(otlpHeadersEnv, "Authorization Basic abc123")
	if err := postOTLP(srv.URL, []byte(`{}`), 0); err == nil {
		t.Error("postOTLP sent with a malformed header spec instead of refusing")
	}
	if reached {
		t.Error("the request went out anyway, unauthenticated; the 401 would read as the collector's fault")
	}
}

// A non-2xx must not report success. This is the only place the tool can tell
// you the export did not land.
func TestPostOTLP_SurfacesANon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("invalid credentials"))
	}))
	defer srv.Close()

	err := postOTLP(srv.URL, []byte(`{}`), 1)
	if err == nil {
		t.Fatal("postOTLP reported success on a 401")
	}
	for _, want := range []string{"401", "invalid credentials"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not carry %q, so the cause is invisible", err, want)
		}
	}
}
