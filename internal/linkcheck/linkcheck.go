// SPDX-License-Identifier: MIT

// Package linkcheck checks the links an answer cites: each must resolve, and a
// quote in the link's sentence must be on the page. Only the URL leaves the machine.
package linkcheck

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/ground"
	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/trust"
)

// Source is the calibration key the check records under.
const Source = "check:links"

const (
	maxLinks     = 20
	maxRedirects = 3
	maxPageBytes = 2 << 20
	fetchTimeout = 10 * time.Second
)

var (
	// errNoLinks is an answer with no URL in it: nothing to check, never a pass.
	errNoLinks = errors.New("no links in the answer")
	// errDenied is the ledger refusing the fetch.
	errDenied = errors.New("the ledger did not allow network access")

	errRedirects = errors.New("too many redirects")
	urlRE        = regexp.MustCompile(`https?://[^\s<>"'“”()\[\]{}]+`)
)

// Gate decides whether one URL may be fetched. LedgerGate asks the MCP ledger and records the answer.
type Gate func(url string) (bool, error)

// LedgerGate asks the ledger's policy for network access to url and records the decision.
func LedgerGate(url string) (bool, error) {
	pol, err := ledger.LoadPolicy(ledger.DefaultPolicyPath())
	if err != nil {
		return false, err
	}
	d, err := ledger.Check(ledger.DefaultPath(), pol, ledger.CheckRequest{
		Agent: "hyctl", Tool: Source, Resource: url, Action: ledger.Network,
	})
	return d == ledger.Allow, err
}

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

// fetch gets url once the gate allows it. An error is not checked: the network or the
// gate said nothing about the link. A reply that is not 2xx, or loops, is a dead link.
func (o *Oracle) fetch(ctx context.Context, url string) (page, error) {
	if o.Gate == nil {
		return page{}, errDenied
	}
	ok, err := o.Gate(url)
	if err != nil {
		return page{}, err
	}
	if !ok {
		return page{}, errDenied
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return page{failed: "is not a valid URL"}, nil
	}
	resp, err := client.Do(req)
	if errors.Is(err, errRedirects) {
		return page{failed: fmt.Sprintf("redirects more than %d times", maxRedirects)}, nil
	}
	if err != nil {
		return page{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return page{failed: "does not resolve: " + resp.Status}, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes+1))
	if err != nil {
		return page{}, err
	}
	truncated := len(body) > maxPageBytes
	if truncated {
		body = body[:maxPageBytes]
	}
	return page{text: pageText(string(body)), truncated: truncated}, nil
}

var client = &http.Client{Timeout: fetchTimeout, CheckRedirect: func(_ *http.Request, via []*http.Request) error {
	if len(via) > maxRedirects {
		return errRedirects
	}
	return nil
}}

var (
	hiddenRE = regexp.MustCompile(`(?is)<(script|style)\b.*?</(script|style)\s*>`)
	tagRE    = regexp.MustCompile(`(?s)<[^>]*>`)
)

// pageText is what a reader sees: scripts, styles and tags dropped, entities decoded.
func pageText(raw string) string {
	return html.UnescapeString(tagRE.ReplaceAllString(hiddenRE.ReplaceAllString(raw, " "), " "))
}
