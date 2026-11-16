// SPDX-License-Identifier: MIT

package linkcheck

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/trust"
)

const article = `<html><head><style>p{}</style><script>var x = "the board voted to close";</script></head>
<body><p>The board   voted to <b>close</b> all editions on March&nbsp;30.</p></body></html>`

// site serves the article at /ok, a 404 at /gone, a loop at /loop and a page over the cap at /big.
func site(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(article)) })
	mux.HandleFunc("/moved", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ok", http.StatusFound) })
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/loop", http.StatusFound) })
	mux.HandleFunc("/big", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(strings.Repeat("x", maxPageBytes) + " the board voted to close"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func allowAll(string) (bool, error) { return true, nil }

func verify(t *testing.T, gate Gate, answer string) (bool, string, error) {
	t.Helper()
	v, err := (&Oracle{Gate: gate}).Verify(context.Background(), answer, trust.Task{})
	return v.Passed, v.Detail, err
}

func TestLinksCheck_Cases(t *testing.T) {
	u := site(t).URL
	cases := []struct {
		name, answer string
		passed       bool
		detail       string
		notChecked   bool
	}{
		{"link ok", "The editions close, per " + u + "/ok.", true, "all 1 links resolved", false},
		{"redirect followed", "See " + u + "/moved for details.", true, "all 1 links resolved", false},
		{"404", "See " + u + "/gone for details.", false, "404", false},
		{"quote present", `The report says "the board voted to close" (` + u + "/ok).", true, "all 1 links resolved", false},
		{"quote missing", `The report says "the board voted to stay open" (` + u + "/ok).", false, "is on none of", false},
		{"quote only in a script", `It said "var x = the board" at ` + u + "/ok.", false, "is on none of", false},
		{"redirect loop", "See " + u + "/loop for details.", false, "redirects more than", false},
		{"oversize page, quote past the cap", `It says "the board voted to close" at ` + u + "/big.", false, "", true},
		{"no links", "The board voted to close.", false, "", true},
	}
	for _, c := range cases {
		passed, detail, err := verify(t, allowAll, c.answer)
		if c.notChecked {
			if err == nil {
				t.Errorf("%s: got a verdict (%v, %q), want not checked", c.name, passed, detail)
			}
			continue
		}
		if err != nil || passed != c.passed || !strings.Contains(detail, c.detail) {
			t.Errorf("%s: passed %v detail %q err %v; want passed %v with %q", c.name, passed, detail, err, c.passed, c.detail)
		}
	}
}

func TestLinksCheck_DeniedByLedgerIsNotChecked(t *testing.T) {
	testutil.NewSandbox(t)
	u := site(t).URL
	pol := `{"default":"allow","rules":[{"action":"network","tool":"` + Source + `","decision":"deny"}]}`
	if err := os.MkdirAll(filepath.Dir(ledger.DefaultPolicyPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ledger.DefaultPolicyPath(), []byte(pol), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := verify(t, LedgerGate(Source), "See "+u+"/ok for details.")
	if !errors.Is(err, errDenied) {
		t.Fatalf("err = %v, want not checked: the ledger denied the fetch", err)
	}
	events, err := ledger.Load(ledger.DefaultPath())
	if err != nil || len(events) != 1 || events[0].Action != ledger.Network || events[0].Decision != ledger.Deny ||
		events[0].Resource != u+"/ok" {
		t.Fatalf("ledger = %+v, %v; want one recorded network denial for the URL alone", events, err)
	}
}

func TestLinksCheck_AllowedFetchIsRecorded(t *testing.T) {
	testutil.NewSandbox(t)
	u := site(t).URL
	if passed, _, err := verify(t, LedgerGate(Source), "See "+u+"/ok and "+u+"/ok again."); err != nil || !passed {
		t.Fatalf("passed %v, err %v", passed, err)
	}
	events, err := ledger.Load(ledger.DefaultPath())
	if err != nil || len(events) != 1 || events[0].Decision != ledger.Allow {
		t.Fatalf("ledger = %+v, %v; want one allowed fetch for the one URL", events, err)
	}
}

func TestLinksCheck_NoNetworkIsNotChecked(t *testing.T) {
	srv := site(t)
	u := srv.URL
	srv.Close()
	if _, _, err := verify(t, allowAll, "See "+u+"/ok for details."); err == nil {
		t.Fatal("an unreachable link read as a verdict, want not checked")
	}
}
