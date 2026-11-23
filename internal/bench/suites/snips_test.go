// SPDX-License-Identifier: MIT

package suites

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/bench"
	"github.com/ankit373/hydra/internal/executor"
)

func TestExtractionSuite_LoadsGoldFieldsPerIntent(t *testing.T) {
	s, items := loadFixture(t, "snips")
	if len(items) != 7 || items[1].ID != "BookRestaurant/0" || items[1].Class != "extraction.snips" {
		t.Fatalf("items %+v", items)
	}
	text, _ := items[1].Text()
	gold, _ := items[1].Label()
	if text != "book a table for 4 at 7 pm in Lisbon" || gold != `{"city":"Lisbon","party_size_number":"4","timeRange":"7 pm"}` {
		t.Fatalf("text %q gold %s", text, gold)
	}
	req, err := s.Prompt(items[1])
	if err != nil || !strings.Contains(req.System, "Allowed keys: city, party_size_number, timeRange.") ||
		!strings.Contains(req.Prompt, "Lisbon") || *req.Temperature != 0 {
		t.Fatalf("prompt %+v, %v", req, err)
	}
}

func TestExtractionSuite_GradeMatchesGoldFields(t *testing.T) {
	s, items := loadFixture(t, "snips")
	it := items[1] // city Lisbon, party_size_number 4, timeRange 7 pm
	for answer, want := range map[string]bench.Grade{
		`{"city": "Lisbon", "party_size_number": "4", "timeRange": "7 pm"}`:                        {Passed: true},
		"```json\n{\"city\": \"lisbon\", \"party_size_number\": 4, \"timeRange\": \"7  PM\"}\n```": {Passed: true},
		`{"city": "Lisbon", "party_size_number": "4", "timeRange": "7 pm", "cuisine": null}`:       {Passed: true},
		`{"city": "Lisbon", "party_size_number": "4"}`:                                             {}, // a gold field missing
		`{"city": "7 pm", "party_size_number": "4", "timeRange": "Lisbon"}`:                        {}, // values swapped
		`{"city": "Lisbon", "party_size_number": "5", "timeRange": "7 pm"}`:                        {},
		`{"city": "Lisbon", "party_size_number": "4", "timeRange": "7 pm", "cuisine": "thai"}`:     {}, // invented
		"a table for four in Lisbon": {Format: true},
	} {
		g, err := s.Grade(context.Background(), it, answer)
		if err != nil {
			t.Fatal(err)
		}
		if g.Passed != want.Passed || g.Format != want.Format || (!g.Passed && g.Detail == "") {
			t.Errorf("Grade(%q) = %+v, want passed=%v format=%v", answer, g, want.Passed, want.Format)
		}
	}
}

func TestExtractionSuite_RefusesUnloadedAndUnknownIntent(t *testing.T) {
	if _, err := Registry()["snips"]().Grade(context.Background(), bench.Item{ID: "GetWeather/0"}, "{}"); err == nil {
		t.Error("graded before the suite loaded its fields")
	}
	s, _ := loadFixture(t, "snips")
	if _, err := s.Prompt(bench.Item{ID: "OrderPizza/0", Input: []byte(`{"text":"x"}`)}); err == nil {
		t.Error("prompted for an intent the suite never loaded")
	}
}

func TestExtractionSuite_RowsRefuseWhatHasNoOneGold(t *testing.T) {
	lock, root := fixtureLock(t), fixtureRoot(t)
	dir := bench.DataDir(root, "snips")
	for name, content := range map[string]string{
		"field filled twice": `{"GetWeather":[{"data":[{"text":"Oslo","entity":"city"},{"text":" or "},{"text":"Bergen","entity":"city"}]}]}`,
		"no tagged field":    `{"GetWeather":[{"data":[{"text":"is it cold"}]}]}`,
		"bad json":           `{"GetWeather":[`,
	} {
		ds := rehash(t, lock["snips"], dir, "validate_GetWeather.json", content)
		if _, err := Registry()["snips"]().Rows(ds, dir); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The fields check holds every value to the utterance: it flags a value the message
// never says and a field left out, and cannot catch a swap of two values it does say.
func TestExtractionSuite_TalliesTheFieldsTheCheckFlagged(t *testing.T) {
	s, items := loadFixture(t, "snips")
	it := items[1] // "book a table for 4 at 7 pm in Lisbon"
	type tally = bench.FieldTally
	for answer, want := range map[string]map[string]tally{
		`{"city": "Lisbon", "party_size_number": "4", "timeRange": "7 pm"}`: {"text": {Right: 2}, "number": {Right: 1}},
		`{"city": "Lisbon", "party_size_number": "5", "timeRange": "7 pm"}`: {"text": {Right: 2}, "number": {Wrong: 1, WrongFlagged: 1}},
		`{"city": "Lisbon", "party_size_number": "4"}`:                      {"text": {Right: 1, Wrong: 1, WrongFlagged: 1}, "number": {Right: 1}},
		`{"city": "7 pm", "party_size_number": "4", "timeRange": "Lisbon"}`: {"text": {Wrong: 2}, "number": {Right: 1}},
		`{"city": "Lisbon", "party_size_number": "4", "timeRange": "7 pm", "cuisine": "thai"}`: {"text": {Right: 2, Wrong: 1, WrongFlagged: 1},
			"number": {Right: 1}},
		`{"city": "Lisbon", "party_size_number": 4, "timeRange": "7 pm", "cuisine": null}`: {"text": {Right: 2},
			"number": {Right: 1, RightFlagged: 1}},
	} {
		g, err := s.Grade(context.Background(), it, answer)
		if err != nil {
			t.Fatal(err)
		}
		if !maps.Equal(g.Fields, want) {
			t.Errorf("Grade(%s) fields = %+v, want %+v", answer, g.Fields, want)
		}
	}
	if g, _ := s.Grade(context.Background(), it, "a table for four"); g.Fields != nil {
		t.Errorf("a format fail tallied %+v", g.Fields)
	}
}

// A second model that reads the utterance correctly catches the swap the source check
// cannot, and the tally keeps both counts so the two can be shown side by side (#2013).
func TestExtractionSuite_SecondModelCatchesWhatTheSourceCheckMisses(t *testing.T) {
	s, items := loadFixture(t, "snips")
	it := items[1] // "book a table for 4 at 7 pm in Lisbon"
	reread := func(reply string) (bench.Ask, *int) {
		calls := 0
		return func(_ context.Context, req executor.Request) (string, error) {
			calls++
			if !strings.Contains(req.Prompt, "Lisbon") || *req.Temperature != 0 || req.MaxTokens == 0 {
				t.Errorf("second request %+v does not carry the utterance, a pinned temperature and a cap", req)
			}
			return reply, nil
		}, &calls
	}
	good := `{"city": "Lisbon", "party_size_number": "4", "timeRange": "7 pm"}`
	type tally = bench.FieldTally
	for name, c := range map[string]struct {
		answer, reply string
		want          map[string]tally
	}{
		"both agree": {good, good, map[string]tally{
			"text": {Right: 2, Second: true}, "number": {Right: 1, Second: true}}},
		"a swap only the second model sees": {
			`{"city": "7 pm", "party_size_number": "4", "timeRange": "Lisbon"}`, good,
			map[string]tally{"text": {Wrong: 2, Second: true, WrongFlaggedSecond: 2}, "number": {Right: 1, Second: true}}},
		"a right field the second model reads differently": {
			good, `{"city": "Lisbon", "party_size_number": "5", "timeRange": "7 pm"}`,
			map[string]tally{"text": {Right: 2, Second: true}, "number": {Right: 1, Second: true, RightFlaggedSecond: 1}}},
		"a reply that is not JSON confirms nothing": {
			good, "I could not find any fields.",
			map[string]tally{"text": {Right: 2, Second: true, RightFlaggedSecond: 2}, "number": {Right: 1, Second: true, RightFlaggedSecond: 1}}},
	} {
		ask, calls := reread(c.reply)
		g, err := s.(bench.SecondGrader).GradeWithSecond(context.Background(), it, c.answer, ask)
		if err != nil || *calls != 1 {
			t.Fatalf("%s: err %v after %d calls, want one", name, err, *calls)
		}
		if !maps.Equal(g.Fields, c.want) {
			t.Errorf("%s: fields = %+v, want %+v", name, g.Fields, c.want)
		}
	}
}

// A second model that does not answer is that item's failure, passed up as the runner reads it.
func TestExtractionSuite_SecondModelFailureIsPassedUp(t *testing.T) {
	s, items := loadFixture(t, "snips")
	want := &bench.GraderError{Kind: "transport", Transient: true}
	_, err := s.(bench.SecondGrader).GradeWithSecond(context.Background(), items[1],
		`{"city": "Lisbon"}`, func(context.Context, executor.Request) (string, error) { return "", want })
	var got *bench.GraderError
	if !errors.As(err, &got) || got != want {
		t.Fatalf("err = %v, want the grader's own", err)
	}
}
