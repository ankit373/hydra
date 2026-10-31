// SPDX-License-Identifier: MIT

package probe

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ankit373/hydra/internal/osv"
	"github.com/ankit373/hydra/internal/security"
)

func fakeOSV(t *testing.T, body string, status int) *[]byte {
	t.Helper()
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	orig := osv.QueryURL
	osv.QueryURL = srv.URL
	t.Cleanup(func() {
		osv.QueryURL = orig
		srv.Close()
	})
	return &captured
}

func TestLookupAdvisories_ChecksAServerItKnowsHowToAskAbout(t *testing.T) {
	fakeOSV(t, `{"vulns":[{"id":"GHSA-x","aliases":["CVE-2026-7482"],"affected":[{
		"package":{"name":"github.com/ollama/ollama","ecosystem":"Go"},
		"ranges":[{"events":[{"introduced":"0"},{"fixed":"0.17.1"}]}]}]}]}`, http.StatusOK)

	got := LookupAdvisories(context.Background(), []security.LocalServer{
		{Kind: "ollama", Version: "0.16.0", Endpoint: "http://localhost:11434"},
	})
	if got[0].AdvisoryState != security.AdvisoriesChecked {
		t.Fatalf("state = %q, want checked", got[0].AdvisoryState)
	}
	if len(got[0].Advisories) != 1 || got[0].Advisories[0].FixedIn != "0.17.1" {
		t.Errorf("advisories = %+v", got[0].Advisories)
	}
}

// Asking without a version returns every advisory ever filed against the
// package, which says nothing about the server running here. It must not be
// silently asked, and must not read as clean either.
func TestLookupAdvisories_NoVersionIsNotAsked(t *testing.T) {
	seen := fakeOSV(t, `{"vulns":[{"id":"GHSA-everything"}]}`, http.StatusOK)

	got := LookupAdvisories(context.Background(), []security.LocalServer{{Kind: "ollama"}})
	if got[0].AdvisoryState != security.AdvisoriesUnknownVersion {
		t.Errorf("state = %q, want unknown-version", got[0].AdvisoryState)
	}
	if len(got[0].Advisories) != 0 {
		t.Errorf("advisories = %+v, want none from a query that should not have run", got[0].Advisories)
	}
	if len(*seen) != 0 {
		t.Errorf("a request was sent with no version: %s", *seen)
	}
}

// llama.cpp publishes a build id and LM Studio publishes nothing OSV tracks, so
// both are unqueryable rather than clean.
func TestLookupAdvisories_AnUnqueryableKindSaysSo(t *testing.T) {
	fakeOSV(t, `{"vulns":[]}`, http.StatusOK)

	got := LookupAdvisories(context.Background(), []security.LocalServer{
		{Kind: "llamacpp", Version: "b4567-abc1234"},
		{Kind: "lmstudio"},
	})
	for _, s := range got {
		if s.AdvisoryState != security.AdvisoriesNotQueryable {
			t.Errorf("%s state = %q, want not-queryable", s.Kind, s.AdvisoryState)
		}
	}
}

// A lookup that did not complete is unchecked. Reporting it as no advisories
// would turn an outage at OSV into a clean bill of health.
func TestLookupAdvisories_AFailedQueryIsNotClean(t *testing.T) {
	fakeOSV(t, `{}`, http.StatusInternalServerError)

	got := LookupAdvisories(context.Background(), []security.LocalServer{
		{Kind: "ollama", Version: "0.33.2"},
	})
	if got[0].AdvisoryState != security.AdvisoriesFailed {
		t.Errorf("state = %q, want failed", got[0].AdvisoryState)
	}
}

func TestLookupAdvisories_CleanServerIsCheckedAndEmpty(t *testing.T) {
	fakeOSV(t, `{"vulns":[]}`, http.StatusOK)

	got := LookupAdvisories(context.Background(), []security.LocalServer{
		{Kind: "ollama", Version: "9.9.9"},
	})
	if got[0].AdvisoryState != security.AdvisoriesChecked || len(got[0].Advisories) != 0 {
		t.Errorf("got %+v, want checked with no advisories", got[0])
	}
}

// The scan result is the caller's; filling it in must not alter what they hold.
func TestLookupAdvisories_DoesNotMutateTheInput(t *testing.T) {
	fakeOSV(t, `{"vulns":[{"id":"GHSA-x"}]}`, http.StatusOK)

	in := []security.LocalServer{{Kind: "ollama", Version: "0.16.0"}}
	_ = LookupAdvisories(context.Background(), in)
	if in[0].AdvisoryState != security.AdvisoriesNotRequested || in[0].Advisories != nil {
		t.Errorf("input was modified: %+v", in[0])
	}
}

func TestLookupAdvisories_NoServersIsNoRequests(t *testing.T) {
	seen := fakeOSV(t, `{"vulns":[]}`, http.StatusOK)

	if got := LookupAdvisories(context.Background(), nil); len(got) != 0 {
		t.Errorf("got %+v, want empty", got)
	}
	if len(*seen) != 0 {
		t.Errorf("a request was sent with no servers: %s", *seen)
	}
}
