// SPDX-License-Identifier: MIT

package recipe

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestValidate_SamplesWithoutCheckRefused(t *testing.T) {
	r := Recipe{Name: "vote", Samples: 5, Stop: StopAll}
	err := r.Validate()
	if err == nil || !strings.Contains(err.Error(), "no check") {
		t.Fatalf("5 samples with no check: err %v, want a refusal naming the missing check", err)
	}
	if err := (Recipe{Name: "zero", Samples: 0, Stop: StopNone}).Validate(); err == nil {
		t.Error("0 samples was accepted")
	}
}

func TestValidate_FirstPassNeedsACheck(t *testing.T) {
	if err := (Recipe{Name: "fp", Samples: 1, Stop: StopFirstPass}).Validate(); err == nil {
		t.Error("first_pass with no check was accepted: nothing can pass")
	}
	ok := Recipe{Name: "fp", Samples: 3, Checks: []string{"ground"}, Stop: StopFirstPass}
	if err := ok.Validate(); err != nil {
		t.Errorf("3 samples, a check and first_pass: %v", err)
	}
}

func TestValidate_CertifiedReservedForNow(t *testing.T) {
	r := Recipe{Name: "cert", Samples: 3, Checks: []string{"ground"}, Stop: StopCertified}
	if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("certified: err %v, want it refused as reserved", err)
	}
}

func TestValidate_RefusesStopsThatCannotWork(t *testing.T) {
	for name, r := range map[string]Recipe{
		"unknown stop":        {Name: "x", Samples: 1, Stop: "majority"},
		"empty stop":          {Name: "x", Samples: 1},
		"budget with no cap":  {Name: "x", Samples: 2, Checks: []string{"ground"}, Stop: StopBudget},
		"none with k samples": {Name: "x", Samples: 2, Checks: []string{"ground"}, Stop: StopNone},
		"all with no check":   {Name: "x", Samples: 1, Stop: StopAll},
	} {
		if err := r.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	capped := Recipe{Name: "x", Samples: 4, Checks: []string{"ground"}, Stop: StopBudget, MaxUSD: 0.05}
	if err := capped.Validate(); err != nil {
		t.Errorf("budget with a cap: %v", err)
	}
}

func TestDefault_IsOneCall(t *testing.T) {
	r := Default(nil)
	if r.Name != "single" || r.Samples != 1 || r.Stop != StopNone || len(r.Checks) != 0 {
		t.Errorf("Default(nil) = %+v, want one call with no stop rule", r)
	}
	checked := Default([]string{"match:pattern=^(yes|no)$"})
	if checked.Samples != 1 || checked.Stop != StopFirstPass {
		t.Errorf("Default with a check = %+v, want one call that stops on its check", checked)
	}
	for _, r := range []Recipe{r, checked} {
		if err := r.Validate(); err != nil {
			t.Errorf("the default recipe is refused: %v", err)
		}
	}
}

func TestRecipe_JSONRoundTrip(t *testing.T) {
	temp := 0.7
	in := Recipe{
		Name: "cheap-k", Head: "ollama/qwen3:8b", Samples: 4, Temperature: &temp,
		Checks: []string{"cmd:go test ./... {file}", "ground"}, Stop: StopFirstPass,
		Fallback: "strong-once", MaxUSD: 0.02,
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Recipe
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round trip changed the recipe:\n in  %+v\n out %+v\n json %s", in, out, b)
	}
	for _, key := range []string{`"samples":4`, `"stop":"first_pass"`, `"fallback":"strong-once"`, `"max_usd":0.02`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("json %s lacks %s", b, key)
		}
	}
}
