// SPDX-License-Identifier: MIT

package checkgen

import (
	"context"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/trust"
)

// sumVerdict runs the sum spec on answer.
func sumVerdict(t *testing.T, spec, answer string) (bool, string) {
	t.Helper()
	o, err := NewSum(spec)
	if err != nil {
		t.Fatal(err)
	}
	v, err := o.Verify(context.Background(), answer, trust.Task{})
	if err != nil {
		t.Fatal(err)
	}
	return v.Passed, v.Detail
}

const receipt = `{"items": [{"amount": "1,000.25"}, {"amount": 150.25}, {"amount": "$50"}], "total": %s}`

func TestSumCheck_ItemsAddUp(t *testing.T) {
	for _, total := range []string{`1200.50`, `"USD 1,200.50"`, `"€1200.5"`} {
		answer := strings.Replace(receipt, "%s", total, 1)
		if passed, detail := sumVerdict(t, "total=items[].amount", answer); !passed {
			t.Errorf("total %s: detail %q, want a pass", total, detail)
		}
	}
}

func TestSumCheck_InventedTotalFails(t *testing.T) {
	answer := strings.Replace(receipt, "%s", `1250.00`, 1)
	passed, detail := sumVerdict(t, "total=items[].amount", answer)
	if passed || detail != "total is 1250.00 but items[].amount adds up to 1200.50" {
		t.Errorf("passed=%v detail=%q, want both numbers", passed, detail)
	}
}

func TestSumCheck_SubtotalPlusTax(t *testing.T) {
	if passed, detail := sumVerdict(t, "total=subtotal,tax", `{"subtotal": 100, "tax": 8.25, "total": 108.25}`); !passed {
		t.Errorf("detail %q, want a pass", detail)
	}
	if passed, _ := sumVerdict(t, "totals.due = totals.net, totals.vat", `{"totals": {"net": 100, "vat": 20, "due": 125}}`); passed {
		t.Error("a nested total that does not add up passed")
	}
}

func TestSumCheck_MissingPath(t *testing.T) {
	for _, tc := range []struct{ spec, answer, want string }{
		{"total=subtotal,tax", `{"subtotal": 100, "total": 108}`, "tax is missing"},
		{"total=items[].amount", `{"items": [{"amount": 5}, {"price": 3}], "total": 8}`, "items[].amount is missing"},
		{"total=items[].amount", `{"items": "two", "total": 8}`, "items is not a list"},
		{"total=subtotal,tax", `{"subtotal": 100, "tax": "about 8", "total": 108}`, "about 8 is not a number"},
		{"total=subtotal,tax", `{"subtotal": 100, "tax": "8 USD", "total": 108}`, "8 USD is not a number"},
		{"total=subtotal,tax", `the total is 108`, "not a JSON object"},
	} {
		if passed, detail := sumVerdict(t, tc.spec, tc.answer); passed || !strings.Contains(detail, tc.want) {
			t.Errorf("%s: passed=%v detail=%q, want a fail naming %q", tc.answer, passed, detail, tc.want)
		}
	}
	for _, spec := range []string{"total", "=a", "total=", "total=a,,b"} {
		if _, err := NewSum(spec); err == nil {
			t.Errorf("spec %q was accepted", spec)
		}
	}
}

func TestSumCheck_RoundingTolerance(t *testing.T) {
	for _, tc := range []struct {
		answer string
		passed bool
	}{
		{`{"a": 3.333, "b": 3.333, "c": 3.333, "total": 10.00}`, true}, // 9.999, within 0.005
		{`{"a": 3.33, "b": 3.33, "c": 3.33, "total": 10.00}`, false},   // 9.99, a cent short
		{`{"a": 0.1, "b": 0.2, "c": 0, "total": 0.3}`, true},           // float addition is not exact
		{`{"a": 4.4, "b": 4.4, "c": 1, "total": 10}`, true},            // 9.8 rounds to the whole 10
		{`{"a": 4.4, "b": 4.4, "c": 0.6, "total": 10}`, false},         // 9.4 does not
	} {
		if passed, detail := sumVerdict(t, "total=a,b,c", tc.answer); passed != tc.passed {
			t.Errorf("%s: passed=%v (%q), want %v", tc.answer, passed, detail, tc.passed)
		}
	}
}
