// SPDX-License-Identifier: MIT

package serve

import (
	"errors"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
)

// deprecatingRouter is a stubRouter that calls every unpinned request naming
// no objective deprecated, as the real adapter does on a default server.
type deprecatingRouter struct{ stubRouter }

func (*deprecatingRouter) Deprecated(r Request) bool {
	return r.Route == (Route{}) && r.Objective == ""
}

// RFC 9745: an @epoch Deprecation date plus a rel="deprecation" Link, on the
// requests that rely on the default and on nothing else, streamed or not,
// answered or failed.
func TestChat_DeprecationHeaderOnlyOnTheImplicitDefault(t *testing.T) {
	for _, tc := range []struct {
		name   string
		r      Router
		header http.Header
		body   string
		want   bool
	}{
		{"implicit default", &deprecatingRouter{stubRouter{answer: Answer{Output: "ok"}}}, nil, oneTurn, true},
		{"implicit default, streamed", &deprecatingRouter{stubRouter{answer: Answer{Output: "ok"}}}, nil, streamTurn, true},
		{"implicit default, failed", &deprecatingRouter{stubRouter{err: errors.New("boom")}}, nil, oneTurn, true},
		{"objective header", &deprecatingRouter{stubRouter{answer: Answer{Output: "ok"}}},
			http.Header{ObjectiveHeader: {"quality"}}, oneTurn, false},
		{"hydra/standard", &deprecatingRouter{stubRouter{answer: Answer{Output: "ok"}}}, nil,
			`{"model":"hydra/standard","messages":[{"role":"user","content":"hi"}]}`, false},
		{"router without the extension", &stubRouter{answer: Answer{Output: "ok"}}, nil, oneTurn, false},
	} {
		w := postWith(t, tc.r, tc.header, tc.body)
		dep, link := w.Header().Get(DeprecationHeader), w.Header().Get("Link")
		if !tc.want {
			if dep != "" || link != "" {
				t.Errorf("%s: Deprecation %q Link %q sent, want neither", tc.name, dep, link)
			}
			continue
		}
		if dep != deprecatedSince || !strings.HasPrefix(dep, "@") {
			t.Errorf("%s: Deprecation %q, want %q", tc.name, dep, deprecatedSince)
		}
		if !strings.Contains(link, `rel="deprecation"`) || !strings.Contains(link, "<https://") {
			t.Errorf("%s: Link %q, want a rel=\"deprecation\" URL", tc.name, link)
		}
	}
}

// The Link must land on the section that explains the change; the README
// anchor it used to name was moved to the docs site (#1796).
func TestDeprecationLink_AnchorExistsOnTheDocsPage(t *testing.T) {
	m := regexp.MustCompile(`<https://hydra\.uvansa\.com/([a-z-]+\.html)#([a-z-]+)>`).FindStringSubmatch(deprecationLink)
	if m == nil {
		t.Fatalf("Link %q does not point at a hydra.uvansa.com page anchor", deprecationLink)
	}
	page, err := os.ReadFile("../../docs/" + m[1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), `id="`+m[2]+`"`) {
		t.Errorf("docs/%s has no id=%q, so the deprecation Link lands nowhere", m[1], m[2])
	}
}
