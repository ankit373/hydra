// SPDX-License-Identifier: MIT

// Package linkcheck checks the links an answer cites: each must resolve, and a
// quote in the link's sentence must be on the page. Only the URL leaves the machine.
package linkcheck

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/ankit373/hydra/internal/ground"
	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/trust"
)

// Source is the calibration key the check records under.
const Source = "check:links"

// maxLinks bounds the fetches one answer can cause.
const maxLinks = 20

var (
	// errNoLinks is an answer with no URL in it: nothing to check, never a pass.
	errNoLinks = errors.New("no links in the answer")
	urlRE      = regexp.MustCompile(`https?://[^\s<>"'“”()\[\]{}]+`)
)

// Oracle fetches every link in an answer, asking Gate first.
type Oracle struct{ Gate Gate }

// Key is the calibration source this oracle records under.
func (o *Oracle) Key() string { return Source }

// page is one fetched link: why it failed, or its text and whether the cap cut it short.
type page struct {
	failed    string
	text      string
	truncated bool
}

// Verify fails on a dead link, or a quote that no live page of its sentence holds. No links,
// a denied fetch or no network is not checked, since none of them says the answer is wrong.
func (o *Oracle) Verify(ctx context.Context, candidate string, _ trust.Task) (oracle.Verdict, error) {
	bySentence := map[string][]string{}
	var order []string
	n := 0
	for _, s := range ground.Sentences(candidate) {
		for _, u := range urlRE.FindAllString(s, -1) {
			if len(bySentence[s]) == 0 {
				order = append(order, s)
			}
			bySentence[s] = append(bySentence[s], strings.TrimRight(u, ".,;:!?"))
			n++
		}
	}
	if n == 0 {
		return oracle.Verdict{}, errNoLinks
	}
	if n > maxLinks {
		return oracle.Verdict{}, fmt.Errorf("partly checked: %d links, at most %d are fetched", n, maxLinks)
	}
	pages := map[string]page{}
	var failed []string
	for _, s := range order {
		missing := ground.Quotes(s)
		truncated := false
		for _, u := range bySentence[s] {
			p, ok := pages[u]
			if !ok {
				var err error
				if p, err = o.fetch(ctx, u); err != nil {
					return oracle.Verdict{}, fmt.Errorf("%s: %w", u, err)
				}
				pages[u] = p
			}
			if p.failed != "" {
				failed = append(failed, u+" "+p.failed)
				continue
			}
			missing = slices.DeleteFunc(missing, func(q string) bool { return ground.QuoteIn(q, p.text) })
			truncated = truncated || p.truncated
		}
		if len(missing) > 0 && truncated {
			return oracle.Verdict{}, fmt.Errorf("a page is over %d bytes and the quote is not in the part read", maxPageBytes)
		}
		for _, q := range missing {
			failed = append(failed, fmt.Sprintf("%q is on none of %s", q, strings.Join(bySentence[s], ", ")))
		}
	}
	if len(failed) > 0 {
		return oracle.Verdict{Detail: "links failed: " + strings.Join(failed, "; ")}, nil
	}
	return oracle.Verdict{Passed: true, Detail: fmt.Sprintf("all %d links resolved", len(pages))}, nil
}

// fetch reads a link's page. An error is not checked; a reply that is not 2xx, or loops, is a dead link.
func (o *Oracle) fetch(ctx context.Context, url string) (page, error) {
	r, err := get(ctx, o.Gate, url, "")
	switch {
	case err != nil:
		return page{}, err
	case r.failed != "":
		return page{failed: r.failed}, nil
	case r.status < 200 || r.status > 299:
		return page{failed: fmt.Sprintf("does not resolve: %d %s", r.status, http.StatusText(r.status))}, nil
	}
	return page{text: pageText(string(r.body)), truncated: r.truncated}, nil
}

var (
	hiddenRE = regexp.MustCompile(`(?is)<(script|style)\b.*?</(script|style)\s*>`)
	tagRE    = regexp.MustCompile(`(?s)<[^>]*>`)
)

// pageText is what a reader sees: scripts, styles and tags dropped, entities decoded.
func pageText(raw string) string {
	return html.UnescapeString(tagRE.ReplaceAllString(hiddenRE.ReplaceAllString(raw, " "), " "))
}
