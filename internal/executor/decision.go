// SPDX-License-Identifier: MIT

package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ankit373/hydra/internal/decision"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/util"
)

// decisionKind prefixes the id of a head that answers typed questions with probabilities, never prose.
const decisionKind = "decision"

// decisionDialect is one wire format a decision head speaks.
type decisionDialect struct {
	path    string
	marshal func(r decision.Request, model string) ([]byte, error)
	parse   func(body []byte, r decision.Request) (decision.Result, error)
	// imageHeader is set to "true" on a request carrying an image, where the API gates images behind one.
	imageHeader string
}

var (
	systemOneHosted = decisionDialect{
		path: "/v1/systemone",
		marshal: func(r decision.Request, model string) ([]byte, error) {
			return decision.MarshalSystemOne(r, model, decision.ImageAsAsset)
		},
		parse:       decision.ParseSystemOneResponse,
		imageHeader: "TypeSafe-Danger-Zone-Images",
	}
	systemOneLocal = decisionDialect{
		path: "/v1/systemone",
		marshal: func(r decision.Request, model string) ([]byte, error) {
			return decision.MarshalSystemOne(r, model, decision.ImageInList)
		},
		parse: decision.ParseSystemOneResponse,
	}
	openAIDecisions = decisionDialect{
		path:    "/v1/decisions",
		marshal: decision.MarshalDecisions,
		parse:   decision.ParseDecisionsResponse,
	}
)

// decisionDialects maps a decision head's provider to the wire format it speaks.
var decisionDialects = map[string]decisionDialect{
	"typesafe": systemOneHosted,
	"openai":   openAIDecisions,
	"sglang":   systemOneLocal,
}

// DecisionExecutor asks a decision head typed questions over HTTP.
type DecisionExecutor struct{}

// Execute asks h every question in r. A distribution that does not sum to 1 is
// refused rather than normalised, since a malformed answer is a broken head, not evidence.
func (DecisionExecutor) Execute(
	ctx context.Context, h provider.Head, r decision.Request,
) (decision.Result, error) {
	fail := func(err error) (decision.Result, error) {
		return decision.Result{}, fmt.Errorf("decision head %s: %w", h.ID, err)
	}
	dialect, ok := decisionDialects[h.Provider]
	if !ok {
		return fail(fmt.Errorf("no decision dialect for provider %q", h.Provider))
	}
	if err := r.Validate(); err != nil {
		return fail(err)
	}
	body, err := dialect.marshal(r, h.Meta["model"])
	if err != nil {
		return fail(err)
	}
	url := strings.TrimRight(h.Endpoint, "/") + dialect.path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fail(err)
	}
	req.Header.Set("Content-Type", "application/json")
	// A local server's provider has no key entry, so no credential is ever sent to one.
	if key := apiKeyFor(h.Provider); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if r.Image != "" && dialect.imageHeader != "" {
		req.Header.Set(dialect.imageHeader, "true")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fail(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return decision.Result{}, httpStatusError(h.ID, resp)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(util.DefaultMaxBytes)+1))
	if err != nil {
		return fail(err)
	}
	res, err := dialect.parse(raw, r)
	if err != nil {
		return fail(err)
	}
	if err = r.CheckResult(res); err != nil {
		return fail(fmt.Errorf("%w; refused rather than normalised", err))
	}
	return res, nil
}
