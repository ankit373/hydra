// SPDX-License-Identifier: MIT

package checkgen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/trust"
)

// SumOracle passes an answer whose total equals the sum of its parts, read
// from the answer alone, so it catches a total the model made up.
type SumOracle struct {
	Source string
	total  string
	parts  []string
}

// NewSum reads total=parts, where parts is a list path such as items[].amount
// or a comma list of field paths such as subtotal,tax.
func NewSum(spec string) (*SumOracle, error) {
	total, rhs, ok := strings.Cut(spec, "=")
	total, rhs = strings.TrimSpace(total), strings.TrimSpace(rhs)
	if !ok || total == "" || rhs == "" {
		return nil, errors.New("sum takes <total>=<list>[].<field> or <total>=<field>,<field>")
	}
	o := &SumOracle{Source: "verifier:sum:" + spec, total: total}
	for _, p := range strings.Split(rhs, ",") {
		if p = strings.TrimSpace(p); p == "" {
			return nil, fmt.Errorf("sum %q: an empty part", spec)
		}
		o.parts = append(o.parts, p)
	}
	return o, nil
}

// Verify adds the parts and compares them with the total, within half of the total's last written digit.
func (o *SumOracle) Verify(_ context.Context, candidate string, _ trust.Task) (oracle.Verdict, error) {
	got, ok := DecodeObject(candidate)
	if !ok {
		return oracle.Verdict{Detail: "the answer is not a JSON object"}, nil
	}
	totals, err := amountsAt(got, o.total)
	if err != nil {
		return oracle.Verdict{Detail: err.Error()}, nil
	}
	if len(totals) != 1 {
		return oracle.Verdict{Detail: fmt.Sprintf("%s is a list, want one amount", o.total)}, nil
	}
	sum := 0.0
	for _, p := range o.parts {
		vs, err := amountsAt(got, p)
		if err != nil {
			return oracle.Verdict{Detail: err.Error()}, nil
		}
		for _, v := range vs {
			sum += v.value
		}
	}
	t := totals[0]
	parts := strings.Join(o.parts, " + ")
	if math.Abs(sum-t.value) > 0.5*math.Pow10(-t.decimals)+1e-9 {
		return oracle.Verdict{Detail: fmt.Sprintf("%s is %s but %s adds up to %.*f",
			o.total, t.written, parts, t.decimals, sum)}, nil
	}
	return oracle.Verdict{Passed: true, Detail: fmt.Sprintf("%s %s = %s", o.total, t.written, parts)}, nil
}

// amount is one number as the answer wrote it, and how many decimals it was written with.
type amount struct {
	value    float64
	written  string
	decimals int
}

// amountsAt follows a dotted path; a segment ending in [] reads every item of that list.
func amountsAt(obj map[string]any, path string) ([]amount, error) {
	vals := []any{obj}
	for _, seg := range strings.Split(path, ".") {
		name, each := strings.CutSuffix(seg, "[]")
		var next []any
		for _, v := range vals {
			m, ok := v.(map[string]any)
			if !ok || m[name] == nil {
				return nil, fmt.Errorf("%s is missing", path)
			}
			if !each {
				next = append(next, m[name])
				continue
			}
			list, ok := m[name].([]any)
			if !ok {
				return nil, fmt.Errorf("%s: %s is not a list", path, name)
			}
			next = append(next, list...)
		}
		vals = next
	}
	out := make([]amount, 0, len(vals))
	for _, v := range vals {
		a, ok := readAmount(v)
		if !ok {
			return nil, fmt.Errorf("%s: %s is not a number", path, valueText(v))
		}
		out = append(out, a)
	}
	return out, nil
}

// currency is a leading symbol or three-letter code, the one thing allowed before an amount.
var currency = regexp.MustCompile(`^(?:[$€£¥₹]|[A-Z]{3}\s)\s*`)

func readAmount(v any) (amount, bool) {
	switch v.(type) {
	case json.Number, string:
	default:
		return amount{}, false
	}
	s := currency.ReplaceAllString(strings.TrimSpace(valueText(v)), "")
	f, ok := number(s)
	if !ok {
		return amount{}, false
	}
	a := amount{value: f, written: s}
	if _, frac, found := strings.Cut(s, "."); found {
		a.decimals = len(frac)
	}
	return a, true
}
