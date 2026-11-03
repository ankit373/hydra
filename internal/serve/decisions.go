// SPDX-License-Identifier: MIT

package serve

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/ankit373/hydra/internal/decide"
	"github.com/ankit373/hydra/internal/decision"
	"github.com/ankit373/hydra/internal/runid"
)

const (
	// DomainHeader names the calibration domain a decision is weighed in.
	DomainHeader = "X-Hydra-Domain"
	// SourcesHeader lists the sources asked, in order, so nothing about the answer is hidden.
	SourcesHeader = "X-Hydra-Decision-Sources"
	// ConfidenceHeader is the combined confidence the answer was returned at.
	ConfidenceHeader = "X-Hydra-Decision-Confidence"
)

// Decider routes one decision. cmd/hydra adapts the real router, as it does for chat.
type Decider interface {
	Decide(ctx context.Context, c decide.Call) (decide.Outcome, error)
}

// clientShape is a wire format a client speaks, so it is answered in its own shape.
type clientShape struct {
	parse    func(body []byte) (decision.Request, error)
	marshal  func(r decision.Request, res decision.Result) ([]byte, error)
	writeErr func(w http.ResponseWriter, status int, msg string)
}

var (
	decisionsShape = clientShape{
		parse:    decision.ParseDecisionsRequest,
		marshal:  decision.MarshalDecisionsResponse,
		writeErr: writeError,
	}
	systemOneShape = clientShape{
		parse:    decision.ParseSystemOneRequest,
		marshal:  decision.MarshalSystemOneResponse,
		writeErr: writeDetail,
	}
)

// decisions answers one decision request in the shape it arrived in.
func decisions(d Decider, shape clientShape) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			shape.writeErr(w, http.StatusMethodNotAllowed, "use POST")
			return
		}
		raw, status, err := readBody(req)
		if err != nil {
			shape.writeErr(w, status, err.Error())
			return
		}
		r, err := shape.parse(raw)
		if err != nil {
			shape.writeErr(w, http.StatusBadRequest, "could not read the request: "+err.Error())
			return
		}

		id := runid.New()
		w.Header().Set(RequestIDHeader, id)
		call := decide.Call{Request: r, Domain: req.Header.Get(DomainHeader), RunID: id}
		out, err := d.Decide(req.Context(), call)
		if err != nil {
			shape.writeErr(w, statusFor(err), err.Error())
			return
		}
		body, err := shape.marshal(r, out.Result)
		if err != nil {
			shape.writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.Header().Set(SourcesHeader, strings.Join(out.Asked, ","))
		w.Header().Set(ConfidenceHeader, strconv.FormatFloat(out.Confidence, 'f', 4, 64))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}
}

// writeDetail is System One's error body, which its clients read instead of OpenAI's envelope.
func writeDetail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"detail": msg})
}
