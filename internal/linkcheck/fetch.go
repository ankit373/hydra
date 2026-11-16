// SPDX-License-Identifier: MIT

package linkcheck

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ankit373/hydra/internal/ledger"
)

const (
	maxRedirects = 3
	maxPageBytes = 2 << 20
	fetchTimeout = 10 * time.Second
)

var (
	errDenied    = errors.New("the ledger did not allow network access")
	errRedirects = errors.New("too many redirects")
)

// Gate decides whether one URL may be fetched.
type Gate func(url string) (bool, error)

// LedgerGate asks the MCP ledger's policy for network access as tool, and records each decision.
func LedgerGate(tool string) Gate {
	return func(url string) (bool, error) {
		pol, err := ledger.LoadPolicy(ledger.DefaultPolicyPath())
		if err != nil {
			return false, err
		}
		d, err := ledger.Check(ledger.DefaultPath(), pol, ledger.CheckRequest{
			Agent: "hyctl", Tool: tool, Resource: url, Action: ledger.Network,
		})
		return d == ledger.Allow, err
	}
}

// reply is what a fetch got: a status and at most maxPageBytes of body, or why the URL failed outright.
type reply struct {
	status    int
	body      []byte
	truncated bool
	failed    string
}

var client = &http.Client{Timeout: fetchTimeout, CheckRedirect: func(_ *http.Request, via []*http.Request) error {
	if len(via) > maxRedirects {
		return errRedirects
	}
	return nil
}}

// get fetches url once gate allows it; only the URL and accept leave the machine. An error
// means nothing was learned about the URL (denied, offline), so a caller reports not checked.
func get(ctx context.Context, gate Gate, url, accept string) (reply, error) {
	if gate == nil {
		return reply{}, errDenied
	}
	ok, err := gate(url)
	if err != nil {
		return reply{}, err
	}
	if !ok {
		return reply{}, errDenied
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return reply{failed: "is not a valid URL"}, nil
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := client.Do(req)
	if errors.Is(err, errRedirects) {
		return reply{failed: fmt.Sprintf("redirects more than %d times", maxRedirects)}, nil
	}
	if err != nil {
		return reply{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes+1))
	if err != nil {
		return reply{}, err
	}
	r := reply{status: resp.StatusCode, body: body}
	if len(body) > maxPageBytes {
		r.body, r.truncated = body[:maxPageBytes], true
	}
	return r, nil
}
