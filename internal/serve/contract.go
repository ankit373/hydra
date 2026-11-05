// SPDX-License-Identifier: MIT

package serve

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/ankit373/hydra/internal/trust"
)

const (
	// TaskClassHeader names the calibration class a chat request routes for,
	// and echoes the class it was routed for.
	TaskClassHeader = "X-Hydra-Task-Class"
	// MaxCostHeader is request only: a per-request cost ceiling a client may
	// use to lower the operator's, never to raise it.
	MaxCostHeader = "X-Hydra-Max-Cost-USD"
)

// Evidence is what the router concluded about a request; nil on an Answer
// means it reported none.
type Evidence struct {
	TaskClass      string `json:"task_class"`
	TaskClassBasis string `json:"task_class_basis"`
}

// setEvidenceHeaders is shared by the buffered and streamed replies, so the
// two can never echo different classes.
func setEvidenceHeaders(h http.Header, ev *Evidence) {
	if ev != nil {
		h.Set(TaskClassHeader, ev.TaskClass)
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

// explicitClass is the class the client named, if any. Every value it sent
// must agree, since quietly picking one would route on a class it did not mean.
func explicitClass(h http.Header, metadata map[string]any) (string, error) {
	type said struct{ from, value string }
	var all []said
	for _, v := range h.Values(TaskClassHeader) {
		all = append(all, said{TaskClassHeader, v})
	}
	// Metadata is the client's own free-form map; only a string here is a class.
	if v, ok := metadata["task_class"].(string); ok {
		all = append(all, said{"metadata.task_class", v})
	}
	var class, from string
	for _, s := range all {
		if strings.TrimSpace(s.value) == "" {
			continue
		}
		c, err := trust.TaskClass(s.value)
		if err != nil {
			return "", fmt.Errorf("%s: %w", s.from, err)
		}
		if class != "" && c != class {
			return "", fmt.Errorf("%s says %q but %s says %q; send one class", from, class, s.from, c)
		}
		class, from = c, s.from
	}
	return class, nil
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

// checkTemperature uses OpenAI's own range, so a value a provider would refuse
// is refused here before any head is paid for.
func checkTemperature(t *float64) error {
	if t != nil && !(*t >= 0 && *t <= 2) {
		return errors.New("temperature must be between 0 and 2")
	}
	return nil
}
