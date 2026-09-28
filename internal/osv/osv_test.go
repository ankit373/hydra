// SPDX-License-Identifier: MIT

package osv

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// realShapedBody is the response OSV actually returns for a vulnerable Ollama,
// trimmed to the fields this package reads. Taken from a live query rather than
// invented, since the shape is the whole risk here.
const realShapedBody = `{"vulns":[{
  "id":"GHSA-x8qc-fggm-mpqg",
  "summary":"Ollama contains a heap out-of-bounds read vulnerability in the GGUF model loader",
  "aliases":["CVE-2026-7482","GO-2026-5748"],
  "affected":[{
    "package":{"name":"github.com/ollama/ollama","ecosystem":"Go"},
    "ranges":[{"events":[{"introduced":"0"},{"fixed":"0.17.1"}]}]
  }]
}]}`

func serve(t *testing.T, body string, status int) (url string, seen *[]byte) {
	t.Helper()
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &captured
}

func TestQueryAt_ParsesTheFieldsAnActionNeeds(t *testing.T) {
	url, _ := serve(t, realShapedBody, http.StatusOK)

	got, err := QueryAt(context.Background(), url, "Go", "github.com/ollama/ollama", "0.16.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d advisories, want 1", len(got))
	}
	a := got[0]
	if a.ID != "GHSA-x8qc-fggm-mpqg" {
		t.Errorf("ID = %q", a.ID)
	}
	// The CVE is the id a reader recognises; GHSA and GO ids are not.
	if a.CVE != "CVE-2026-7482" {
		t.Errorf("CVE = %q, want CVE-2026-7482 from the aliases", a.CVE)
	}
	if a.FixedIn != "0.17.1" {
		t.Errorf("FixedIn = %q, want 0.17.1", a.FixedIn)
	}
	if !a.Fixed() {
		t.Error("Fixed() = false on an advisory with a fix")
	}
}

// An advisory with no fixed event is unfixed upstream, and must not borrow a
// version from anywhere: a current Ollama carries nine of these, and rendering
// them as upgradeable would send people after a release that does not exist.
func TestQueryAt_NoFixedEventMeansNoFix(t *testing.T) {
	body := `{"vulns":[{"id":"GO-2025-3557","affected":[{
		"package":{"name":"github.com/ollama/ollama","ecosystem":"Go"},
		"ranges":[{"events":[{"introduced":"0"}]}]}]}]}`
	url, _ := serve(t, body, http.StatusOK)

	got, err := QueryAt(context.Background(), url, "Go", "github.com/ollama/ollama", "0.33.2")
	if err != nil {
		t.Fatal(err)
	}
	if got[0].FixedIn != "" {
		t.Errorf("FixedIn = %q, want empty", got[0].FixedIn)
	}
	if got[0].Fixed() {
		t.Error("Fixed() = true with no fixed event")
	}
}

// A fix in some other package listed by the same advisory is not a fix here.
func TestQueryAt_AFixForAnotherPackageIsNotAFixForThisOne(t *testing.T) {
	body := `{"vulns":[{"id":"GHSA-x","affected":[
		{"package":{"name":"some/other","ecosystem":"Go"},
		 "ranges":[{"events":[{"introduced":"0"},{"fixed":"9.9.9"}]}]},
		{"package":{"name":"github.com/ollama/ollama","ecosystem":"Go"},
		 "ranges":[{"events":[{"introduced":"0"}]}]}]}]}`
	url, _ := serve(t, body, http.StatusOK)

	got, err := QueryAt(context.Background(), url, "Go", "github.com/ollama/ollama", "0.33.2")
	if err != nil {
		t.Fatal(err)
	}
	if got[0].FixedIn != "" {
		t.Errorf("FixedIn = %q, want empty: 9.9.9 fixes a different package", got[0].FixedIn)
	}
}

// The version is what makes the answer about the server actually running, so it
// has to reach the wire.
func TestQueryAt_SendsTheVersionWhenGiven(t *testing.T) {
	url, seen := serve(t, `{"vulns":[]}`, http.StatusOK)

	if _, err := QueryAt(context.Background(), url, "Go", "github.com/ollama/ollama", "0.33.2"); err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	if err := json.Unmarshal(*seen, &sent); err != nil {
		t.Fatal(err)
	}
	if sent["version"] != "0.33.2" {
		t.Errorf("request carried version %v, want 0.33.2: %s", sent["version"], *seen)
	}
}

// With no version the field must be absent rather than empty: OSV reads an
// empty string as a version and matches nothing.
func TestQueryAt_OmitsTheVersionWhenNotGiven(t *testing.T) {
	url, seen := serve(t, `{"vulns":[]}`, http.StatusOK)

	if _, err := QueryAt(context.Background(), url, "npm", "left-pad", ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(*seen), "version") {
		t.Errorf("request carried a version field with none given: %s", *seen)
	}
}

// A failed lookup is an error, never an empty result: empty reads as clean.
func TestQueryAt_ANonOKStatusIsAnError(t *testing.T) {
	url, _ := serve(t, `{}`, http.StatusInternalServerError)

	got, err := QueryAt(context.Background(), url, "Go", "github.com/ollama/ollama", "0.33.2")
	if err == nil {
		t.Errorf("got %v and no error; an unanswered query must not read as no advisories", got)
	}
}

func TestQueryAt_MalformedBodyIsAnError(t *testing.T) {
	url, _ := serve(t, `not json`, http.StatusOK)

	if _, err := QueryAt(context.Background(), url, "Go", "x", "1"); err == nil {
		t.Error("a malformed body must not read as no advisories")
	}
}

func TestQueryAt_RequiresEcosystemAndName(t *testing.T) {
	url, _ := serve(t, `{"vulns":[]}`, http.StatusOK)

	if _, err := QueryAt(context.Background(), url, "", "x", ""); err == nil {
		t.Error("no ecosystem should be refused")
	}
	if _, err := QueryAt(context.Background(), url, "Go", "", ""); err == nil {
		t.Error("no name should be refused")
	}
}

func TestQueryAt_NoAdvisoriesIsEmptyNotAnError(t *testing.T) {
	url, _ := serve(t, `{"vulns":[]}`, http.StatusOK)

	got, err := QueryAt(context.Background(), url, "Go", "github.com/ollama/ollama", "0.33.2")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %d, want 0", len(got))
	}
}
