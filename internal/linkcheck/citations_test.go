// SPDX-License-Identifier: MIT

package linkcheck

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ankit373/hydra/internal/trust"
)

// services stands in for doi.org and the arXiv API, counting the requests each gets.
func services(t *testing.T) (doiHits, arxivHits *atomic.Int32) {
	t.Helper()
	doiHits, arxivHits = &atomic.Int32{}, &atomic.Int32{}
	doi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		doiHits.Add(1)
		if r.Header.Get("Accept") != doiAccept {
			w.WriteHeader(http.StatusNotAcceptable)
			return
		}
		switch r.URL.Path {
		case "/10.1126/science.169.3946.635":
			fmt.Fprint(w, `{"title":"The Structure of Ordinary Water"}`)
		case "/10.5555/down":
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	arxiv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arxivHits.Add(1)
		fmt.Fprint(w, `<feed xmlns="http://www.w3.org/2005/Atom">`)
		if strings.Contains(r.URL.Query().Get("id_list"), "1706.03762") {
			fmt.Fprint(w, `<entry><id>http://arxiv.org/abs/1706.03762v7</id><title>Attention Is All
  You Need</title></entry>`)
		}
		fmt.Fprint(w, `</feed>`)
	}))
	t.Cleanup(doi.Close)
	t.Cleanup(arxiv.Close)
	oldDOI, oldArxiv := doiBase, arxivBase
	doiBase, arxivBase = doi.URL+"/", arxiv.URL+"/api/query?id_list="
	t.Cleanup(func() { doiBase, arxivBase = oldDOI, oldArxiv })
	return doiHits, arxivHits
}

func cite(t *testing.T, c *Citations, answer string) (bool, string, error) {
	t.Helper()
	v, err := c.Verify(context.Background(), answer, trust.Task{})
	return v.Passed, v.Detail, err
}

func TestCitationsCheck_Cases(t *testing.T) {
	services(t)
	cases := []struct {
		name, answer string
		passed       bool
		detail       string
	}{
		{"doi exists, title matches", `See "The structure of ordinary water!" (doi:10.1126/science.169.3946.635).`, true, "all 1 citations exist"},
		{"arxiv exists, title matches", `Transformers came from "Attention is all you need" (arXiv:1706.03762).`, true, "all 1 citations exist"},
		{"exists, no title given", "Per arXiv:1706.03762v5, attention suffices.", true, "all 1 citations exist"},
		{"doi missing", "As shown in doi:10.9999/made.up.2024, it works.", false, "doi 10.9999/made.up.2024 does not exist"},
		{"arxiv missing", "As shown in arXiv:2401.99999, it works.", false, "arxiv 2401.99999 does not exist"},
		{"title differs", `See "The structure of liquid water" (10.1126/science.169.3946.635).`, false, `is titled "The Structure of Ordinary Water"`},
	}
	for _, c := range cases {
		passed, detail, err := cite(t, &Citations{Gate: allowAll}, c.answer)
		if err != nil || passed != c.passed || !strings.Contains(detail, c.detail) {
			t.Errorf("%s: passed %v detail %q err %v; want passed %v with %q", c.name, passed, detail, err, c.passed, c.detail)
		}
	}
}

func TestCitationsCheck_ServiceDownIsNotChecked(t *testing.T) {
	services(t)
	if _, _, err := cite(t, &Citations{Gate: allowAll}, "See doi:10.5555/down for it."); err == nil {
		t.Error("a 503 from doi.org read as a verdict")
	}
	doiBase = "http://127.0.0.1:1/"
	if _, _, err := cite(t, &Citations{Gate: allowAll}, "See doi:10.1126/science.169.3946.635 for it."); err == nil {
		t.Error("an unreachable doi.org read as a verdict")
	}
	if _, _, err := cite(t, &Citations{Gate: allowAll}, "No references here."); err == nil {
		t.Error("an answer with no ids read as a verdict")
	}
	deny := func(string) (bool, error) { return false, nil }
	if _, _, err := cite(t, &Citations{Gate: deny}, "See arXiv:1706.03762."); err == nil {
		t.Error("a denied lookup read as a verdict")
	}
}

func TestCitationsCheck_CachedForADay(t *testing.T) {
	doiHits, arxivHits := services(t)
	c := &Citations{Gate: allowAll, CacheDir: t.TempDir()}
	answer := "Per doi:10.1126/science.169.3946.635 and arXiv:1706.03762 and arXiv:2401.99999, it holds."
	for range 2 {
		if _, _, err := cite(t, c, answer); err != nil {
			t.Fatal(err)
		}
	}
	if doiHits.Load() != 1 || arxivHits.Load() != 1 {
		t.Fatalf("doi.org hit %d times, arXiv %d; want one each: the rerun reads the cache and arXiv ids go in one request",
			doiHits.Load(), arxivHits.Load())
	}
}
