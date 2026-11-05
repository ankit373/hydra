// SPDX-License-Identifier: MIT

package serve

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/trust"
)

const (
	// TaskClassHeader names the calibration class a chat request routes for,
	// and echoes the class it was routed for.
	TaskClassHeader = "X-Hydra-Task-Class"
	// MaxCostHeader is request only: a per-request cost ceiling a client may
	// use to lower the operator's, never to raise it.
	MaxCostHeader = "X-Hydra-Max-Cost-USD"
	// ObjectiveHeader names what an unpinned route optimises, and echoes the
	// objective that ordered the candidates.
	ObjectiveHeader = "X-Hydra-Objective"
	// BasisHeader says whether the answering head's P(pass) was measured in
	// this class or is the catalogue's prior.
	BasisHeader = "X-Hydra-P-Pass-Basis"
)

// Evidence is what the router concluded about a request; nil on an Answer
// means it reported none.
type Evidence struct {
	TaskClass      string `json:"task_class,omitempty"`
	TaskClassBasis string `json:"task_class_basis,omitempty"`
	// Objective is empty on a pinned route, where nothing was ordered by one.
	Objective  string `json:"objective,omitempty"`
	PPassBasis string `json:"p_pass_basis,omitempty"`
}

// setEvidenceHeaders is shared by the buffered and streamed replies, so the
// two can never echo different values.
func setEvidenceHeaders(h http.Header, ev *Evidence) {
	if ev == nil {
		return
	}
	setNonEmpty(h, TaskClassHeader, ev.TaskClass)
	setNonEmpty(h, ObjectiveHeader, ev.Objective)
	setNonEmpty(h, BasisHeader, ev.PPassBasis)
}

func setNonEmpty(h http.Header, name, v string) {
	if v != "" {
		h.Set(name, v)
	}
}

// writeEvidence adds the body's `hydra` block beside the headers.
func writeEvidence(w http.ResponseWriter, body map[string]any, ev *Evidence) {
	if ev == nil {
		return
	}
	setEvidenceHeaders(w.Header(), ev)
	body["hydra"] = ev
}

// explicitClass is the class the client named in TaskClassHeader, if any.
// Every value it sent must agree, since quietly picking one would route on a
// class it did not mean.
func explicitClass(h http.Header) (string, error) {
	var class string
	for _, v := range h.Values(TaskClassHeader) {
		if strings.TrimSpace(v) == "" {
			continue
		}
		c, err := trust.TaskClass(v)
		if err != nil {
			return "", fmt.Errorf("%s: %w", TaskClassHeader, err)
		}
		if class != "" && c != class {
			return "", fmt.Errorf("%s was sent as both %q and %q; send one class", TaskClassHeader, class, c)
		}
		class = c
	}
	return class, nil
}

// objective reads ObjectiveHeader; empty means the client left it to the route.
// Every value sent must parse and agree, as with the class.
func objective(h http.Header) (rank.Objective, error) {
	var obj rank.Objective
	for _, v := range h.Values(ObjectiveHeader) {
		o, err := rank.ParseObjective(v)
		if err != nil {
			return "", fmt.Errorf("%s: %w", ObjectiveHeader, err)
		}
		if o == "" {
			continue
		}
		if obj != "" && o != obj {
			return "", fmt.Errorf("%s was sent as both %q and %q; send one objective", ObjectiveHeader, obj, o)
		}
		obj = o
	}
	return obj, nil
}

// maxCost reads MaxCostHeader; 0 means the client sent none. Zero is refused
// as a value, because "spend nothing" and "no ceiling" must not share a spelling.
func maxCost(h http.Header) (float64, error) {
	var ceiling float64
	var first string
	for _, v := range h.Values(MaxCostHeader) {
		if v = strings.TrimSpace(v); v == "" {
			continue
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 {
			return 0, fmt.Errorf("%s must be a finite number of dollars above 0; got %q", MaxCostHeader, v)
		}
		if first != "" && f != ceiling {
			return 0, fmt.Errorf("%s was sent as both %q and %q; send one ceiling", MaxCostHeader, first, v)
		}
		ceiling, first = f, v
	}
	return ceiling, nil
}

// temperature reads the body's temperature in OpenAI's own range, so a value a
// provider would refuse is refused before any head is paid for. Absent or null
// is nil, the provider default.
func temperature(raw json.RawMessage) (*float64, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var t float64
	if err := json.Unmarshal(raw, &t); err != nil || !(t >= 0 && t <= 2) {
		return nil, fmt.Errorf("temperature must be a number between 0 and 2; got %s", raw)
	}
	return &t, nil
}
