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

// writeEvidence is the one carrier of Evidence, header and body alike, so the
// two can never name different classes.
func writeEvidence(w http.ResponseWriter, body map[string]any, ev *Evidence) {
	if ev == nil {
		return
	}
	w.Header().Set(TaskClassHeader, ev.TaskClass)
	body["hydra"] = ev
}

// explicitClass is the class the client named, if any. Every spelling it sent
// must agree, since quietly picking one would route on a class it did not mean.
func explicitClass(h http.Header, metadata map[string]string) (string, error) {
	sources := []struct{ name, value string }{
		{TaskClassHeader, h.Get(TaskClassHeader)},
		{DomainHeader, h.Get(DomainHeader)},
		{"metadata.task_class", metadata["task_class"]},
	}
	var class, from string
	for _, s := range sources {
		if strings.TrimSpace(s.value) == "" {
			continue
		}
		c, err := trust.TaskClass(s.value)
		if err != nil {
			return "", fmt.Errorf("%s: %w", s.name, err)
		}
		if class != "" && c != class {
			return "", fmt.Errorf("%s says %q but %s says %q; send one class", from, class, s.name, c)
		}
		class, from = c, s.name
	}
	return class, nil
}

// maxCost reads MaxCostHeader; 0 means the client set no ceiling.
func maxCost(h http.Header) (float64, error) {
	v := strings.TrimSpace(h.Get(MaxCostHeader))
	if v == "" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return 0, fmt.Errorf("%s must be a finite number of dollars, 0 or more; got %q", MaxCostHeader, v)
	}
	return f, nil
}

// checkTemperature uses OpenAI's own range, so a value a provider would refuse
// is refused here before any head is paid for.
func checkTemperature(t *float64) error {
	if t != nil && !(*t >= 0 && *t <= 2) {
		return errors.New("temperature must be between 0 and 2")
	}
	return nil
}
