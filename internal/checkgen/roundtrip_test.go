// SPDX-License-Identifier: MIT

package checkgen

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/trust"
	"github.com/ankit373/hydra/internal/util"
)

var order = "Pull the order details.\n" + util.WrapUntrusted("EMAIL",
	"Order 7781 was placed on 2026-03-02 and ships on 2026-03-09 to Lisbon.")

const orderFields = `[{"name": "order_date", "type": "string", "required": true, "from_source": true},
 {"name": "ship_date", "type": "string", "required": true, "from_source": true},
 {"name": "city", "type": "string", "required": false, "from_source": true}]`

// reExtract answers the round trip's question with reply and counts the calls.
type reExtract struct {
	reply string
	calls int
	gen   GenInfo
}

func (r *reExtract) ask(_ context.Context, gen GenInfo, prompt string) (string, error) {
	r.calls++
	r.gen = gen
	if !strings.Contains(prompt, "order_date (string), ship_date (string), city (string)") || !strings.Contains(prompt, "ships on 2026-03-09") {
		return "", context.Canceled
	}
	return r.reply, nil
}

func roundtripOf(t *testing.T, r *reExtract) *roundtripOracle {
	t.Helper()
	specs, err := roundtripGen{}.Generate(context.Background(), Task{
		Text: order, Writer: GenInfo{Head: "local:qwen", Family: "qwen"},
		Ask: func(context.Context, string) (string, error) { return orderFields, nil },
	})
	if err != nil || len(specs) != 1 || specs[0].Check() != "gen:roundtrip" {
		t.Fatalf("specs = %+v, %v", specs, err)
	}
	o, err := roundtripGen{}.Oracle(specs[0], Env{Ask: r.ask})
	if err != nil {
		t.Fatal(err)
	}
	return o.(*roundtripOracle)
}

const reExtracted = `{"order_date": "2026-03-02", "ship_date": "2026-03-09", "city": "Lisbon"}`

func TestRoundTrip_SwappedFieldsFail(t *testing.T) {
	r := &reExtract{reply: reExtracted}
	// Both dates are in the source, so the fields check passes this; only the round trip sees the swap.
	swapped := `{"order_date": "2026-03-09", "ship_date": "2026-03-02", "city": "Lisbon"}`
	v, err := roundtripOf(t, r).Verify(context.Background(), swapped, trust.Task{})
	if err != nil || v.Passed || !strings.Contains(v.Detail, "order_date") || !strings.Contains(v.Detail, "ship_date") {
		t.Fatalf("verdict %+v, %v; want both dates named", v, err)
	}
	if r.gen.Head != "local:qwen" {
		t.Errorf("re-extracted with %q, want the spec's head", r.gen.Head)
	}
}

func TestRoundTrip_SameValuesPass(t *testing.T) {
	r := &reExtract{reply: "```json\n" + reExtracted + "\n```"}
	// city is optional, so a different city does not fail the round trip.
	same := `{"order_date": "2026-03-02", "ship_date": " 2026-03-09", "city": "Porto"}`
	v, err := roundtripOf(t, r).Verify(context.Background(), same, trust.Task{})
	if err != nil || !v.Passed || r.calls != 1 {
		t.Fatalf("verdict %+v, %v after %d calls", v, err, r.calls)
	}
	missing := `{"order_date": "2026-03-02"}`
	if v, _ := roundtripOf(t, r).Verify(context.Background(), missing, trust.Task{}); v.Passed {
		t.Error("an answer missing a required field passed")
	}
}

func TestRoundTrip_OnlyRunsWhenAsked(t *testing.T) {
	var kinds []string
	for _, g := range For("extraction.invoices") {
		kinds = append(kinds, g.Kind())
	}
	if slices.Contains(kinds, "roundtrip") || !slices.Contains(kinds, "fields") {
		t.Fatalf("For(extraction) = %v; want fields and not the paid round trip", kinds)
	}
	if g, ok := Named("roundtrip"); !ok || g.Kind() != "roundtrip" {
		t.Fatal("Named(roundtrip) did not find the round trip")
	}
}

func TestRoundTrip_NotCheckedWithoutAModelOrAnswer(t *testing.T) {
	r := &reExtract{reply: "I could not find those."}
	if _, err := roundtripOf(t, r).Verify(context.Background(), reExtracted, trust.Task{}); err == nil {
		t.Error("a re-extraction that was not JSON read as a verdict")
	}
	specs, _ := roundtripGen{}.Generate(context.Background(), Task{
		Text: order, Ask: func(context.Context, string) (string, error) { return orderFields, nil },
	})
	if _, err := (roundtripGen{}).Oracle(specs[0], Env{}); err == nil {
		t.Error("a round trip with no model was built")
	}
	noSource, err := roundtripGen{}.Generate(context.Background(), Task{
		Text: "list the order fields", Ask: func(context.Context, string) (string, error) { return orderFields, nil },
	})
	if err != nil || noSource != nil {
		t.Errorf("no fenced source: %+v, %v; want no spec", noSource, err)
	}
	if v, err := roundtripOf(t, &reExtract{reply: reExtracted}).Verify(context.Background(), "the 2nd of March", trust.Task{}); err != nil || v.Passed {
		t.Errorf("a prose answer: %+v, %v", v, err)
	}
}

func TestRoundtrip_PerFieldAgreement(t *testing.T) {
	o := roundtripOf(t, &reExtract{reply: reExtracted})
	answer := `{"order_date": "2026-03-02", "ship_date": "2026-03-02", "city": "Porto"}`
	if v, err := o.Verify(context.Background(), answer, trust.Task{}); err != nil || v.Passed {
		t.Fatalf("verdict %+v, %v; want a fail on ship_date", v, err)
	}
	if want := "order_date=agreed ship_date=differs city=not_compared"; statuses(o.last) != want {
		t.Fatalf("fields = %q, want %q", statuses(o.last), want)
	}
	if d := o.last[1].Detail; !strings.Contains(d, `"2026-03-02"`) || !strings.Contains(d, `"2026-03-09"`) {
		t.Errorf("differs detail %q does not show both values", d)
	}
}

func TestFieldVerdicts_MergeBothChecks(t *testing.T) {
	ran := func(kind string, fields ...FieldVerdict) Result {
		return Result{Spec: Spec{Kind: kind}, Fields: fields}
	}
	fields := ran("fields",
		FieldVerdict{Name: "vendor", Status: FieldOK, InSource: true},
		FieldVerdict{Name: "total", Status: FieldOK, InSource: true},
		FieldVerdict{Name: "paid", Status: FieldOK},
		FieldVerdict{Name: "date", Status: FieldOK, InSource: true},
		FieldVerdict{Name: "ref", Status: FieldMissing, Detail: "is missing"},
		FieldVerdict{Name: "note", Status: FieldAbsent})
	roundtrip := ran("roundtrip",
		FieldVerdict{Name: "vendor", Status: FieldAgreed},
		FieldVerdict{Name: "total", Status: FieldDiffers, Detail: `answer "9", re-extracted "12"`},
		FieldVerdict{Name: "paid", Status: FieldAgreed},
		FieldVerdict{Name: "date", Status: FieldNotCompared},
		FieldVerdict{Name: "ref", Status: FieldDiffers},
		FieldVerdict{Name: "note", Status: FieldNotCompared})
	got, merged := MergeFields([]Result{fields, {Spec: Spec{Kind: "fields"}, Err: errNoModel}, roundtrip})
	want := "vendor=in_source_and_agreed total=differs paid=agreed date=in_source ref=missing note=absent"
	if !merged || statuses(got) != want {
		t.Fatalf("merged=%v fields=%q, want %q", merged, statuses(got), want)
	}
	if only, merged := MergeFields([]Result{fields}); merged || len(only) != 6 || only[0].Status != FieldOK {
		t.Errorf("fields check alone = %q, merged=%v; want its own list unchanged", statuses(only), merged)
	}
}

// A caller with the field list and the source can run the round trip without a spec (#2013).
func TestJudgeRoundtrip_ComparesRequiredFieldsAndNamesAnUnreadableReply(t *testing.T) {
	fields := []Field{{Name: "city", Type: "string", Required: true}, {Name: "day", Type: "string"}}
	reply := func(s string) func(context.Context, string) (string, error) {
		return func(_ context.Context, prompt string) (string, error) {
			if !strings.Contains(prompt, "city (string), day (string)") || !strings.Contains(prompt, "SOURCE") {
				t.Errorf("prompt %q does not list the fields and carry the source", prompt)
			}
			return s, nil
		}
	}
	got, err := JudgeRoundtrip(context.Background(), fields, "visit Lisbon", `{"city": "Lisbon"}`, reply(`{"city": "lisbon"}`))
	if err != nil || statuses(got) != "city=agreed day=not_compared" {
		t.Fatalf("agreeing reply: %v, %v", statuses(got), err)
	}
	got, err = JudgeRoundtrip(context.Background(), fields, "visit Lisbon", `{"city": "Lisbon"}`, reply(`{"city": "Porto"}`))
	if err != nil || statuses(got) != "city=differs day=not_compared" {
		t.Fatalf("differing reply: %v, %v", statuses(got), err)
	}
	if _, err := JudgeRoundtrip(context.Background(), fields, "x", `{"city": "Lisbon"}`, reply("no idea")); !errors.Is(err, ErrRoundtripReply) {
		t.Errorf("an unreadable reply: err = %v, want ErrRoundtripReply", err)
	}
	if got, err := JudgeRoundtrip(context.Background(), fields, "x", "not json", reply("{}")); got != nil || err != nil {
		t.Errorf("an answer that is not an object: %v, %v; want none", got, err)
	}
}
