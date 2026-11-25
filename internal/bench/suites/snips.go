// SPDX-License-Identifier: MIT

package suites

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ankit373/hydra/internal/bench"
	"github.com/ankit373/hydra/internal/checkgen"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/util"
)

// snipsAnswerTokens bounds a JSON object of at most a handful of short fields.
const snipsAnswerTokens = 160

// snipsIntents are the seven validate files, in the order their rows are numbered.
var snipsIntents = []string{
	"AddToPlaylist", "BookRestaurant", "GetWeather", "PlayMusic",
	"RateBook", "SearchCreativeWork", "SearchScreeningEvent",
}

// snips is the Snips NLU benchmark's validate split as extraction: the slots each
// utterance fills are its gold fields. Ids are <intent>/<row>.
type snips struct {
	fields map[string][]string // intent -> every slot its utterances fill, sorted
}

func (*snips) Name() string     { return "snips" }
func (*snips) Class() string    { return "extraction.snips" }
func (*snips) Labels() []string { return nil }

func snipsFile(intent string) string { return "validate_" + intent + ".json" }

// snipsFiles is every file the suite reads, one per intent.
func snipsFiles() []string {
	files := make([]string, len(snipsIntents))
	for i, intent := range snipsIntents {
		files[i] = snipsFile(intent)
	}
	return files
}

// Rows reads each utterance's chunks back into one text and its tagged chunks into
// gold fields. The gold is stored as a JSON object, keys sorted, as the row's label.
func (s *snips) Rows(ds bench.Dataset, dataDir string) ([]bench.Row, error) {
	if err := ds.RequireFiles(s.Name(), snipsFiles()...); err != nil {
		return nil, err
	}
	s.fields = map[string][]string{}
	var rows []bench.Row
	for _, intent := range snipsIntents {
		raw, err := ds.Open(dataDir, snipsFile(intent))
		if err != nil {
			return nil, err
		}
		var file map[string][]struct {
			Data []struct{ Text, Entity string }
		}
		if err := json.Unmarshal(raw, &file); err != nil {
			return nil, fmt.Errorf("suites: snips %s: %w", snipsFile(intent), err)
		}
		recs := file[intent]
		seen := map[string]bool{}
		w := width(len(recs))
		for i, r := range recs {
			id := fmt.Sprintf("%s/%0*d", intent, w, i)
			var text strings.Builder
			gold := map[string]string{}
			for _, c := range r.Data {
				text.WriteString(c.Text)
				if c.Entity == "" {
					continue
				}
				if _, dup := gold[c.Entity]; dup {
					return nil, fmt.Errorf("suites: snips %s fills %s twice, so it has no one gold value", id, c.Entity)
				}
				gold[c.Entity] = strings.TrimSpace(c.Text)
				seen[c.Entity] = true
			}
			if len(gold) == 0 || strings.TrimSpace(text.String()) == "" {
				return nil, fmt.Errorf("suites: snips %s has no text or no tagged field", id)
			}
			label, _ := json.Marshal(gold) // map keys marshal sorted
			rows = append(rows, bench.Row{ID: id, Text: strings.TrimSpace(text.String()), Label: string(label)})
		}
		s.fields[intent] = slices.Sorted(maps.Keys(seen))
	}
	return rows, nil
}

// Prompt names every field the item's intent can fill, so the answer has to tell
// which ones this utterance gives and copy their values word for word.
func (s *snips) Prompt(it bench.Item) (executor.Request, error) {
	text, err := it.Text()
	if err != nil {
		return executor.Request{}, err
	}
	fields, err := s.fieldsOf(it)
	if err != nil {
		return executor.Request{}, err
	}
	zero := 0.0
	return executor.Request{
		System: "Extract fields from the user's message. Reply with one JSON object and nothing else.\n" +
			"Allowed keys: " + strings.Join(fields, ", ") + ".\n" +
			"Copy each value word for word from the message. Leave out every key the message gives no value for.",
		Prompt:      util.WrapUntrusted("utterance", text),
		Temperature: &zero,
		MaxTokens:   snipsAnswerTokens,
	}, nil
}

// Grade passes only when every gold field matches, compared as checkgen's fields
// check compares values, and no other key carries a value. Not JSON is a format fail.
// It also runs the fields check on the answer and tallies which right and wrong fields it flagged.
func (s *snips) Grade(ctx context.Context, it bench.Item, answer string) (bench.Grade, error) {
	return s.grade(ctx, it, answer, nil)
}

// GradeWithSecond is Grade with a second model re-extracting the answer's fields, and the
// tally also counting what was flagged once its disagreement is added to the fields check.
func (s *snips) GradeWithSecond(ctx context.Context, it bench.Item, answer string, ask bench.Ask) (bench.Grade, error) {
	return s.grade(ctx, it, answer, ask)
}

func (s *snips) grade(ctx context.Context, it bench.Item, answer string, ask bench.Ask) (bench.Grade, error) {
	fields, err := s.fieldsOf(it)
	if err != nil {
		return bench.Grade{}, err
	}
	label, err := it.Label()
	if err != nil {
		return bench.Grade{}, err
	}
	var gold map[string]string
	if err := json.Unmarshal([]byte(label), &gold); err != nil {
		return bench.Grade{}, fmt.Errorf("suites: snips %s gold: %w", it.ID, err)
	}
	got, ok := checkgen.DecodeObject(answer)
	if !ok {
		return bench.Grade{Format: true, Detail: "the answer is not a JSON object"}, nil
	}
	text, err := it.Text()
	if err != nil {
		return bench.Grade{}, err
	}
	second, err := s.secondOpinion(ctx, fields, text, answer, got, ask)
	if err != nil {
		return bench.Grade{}, err
	}
	g := bench.Grade{Passed: true, Fields: tallyFields(fields, text, answer, gold, got, second)}
	for _, k := range slices.Sorted(maps.Keys(gold)) {
		v, ok := got[k]
		if !ok || v == nil {
			g.Passed, g.Detail = false, fmt.Sprintf("field %q is missing, gold %q", k, gold[k])
			return g, nil
		}
		if !checkgen.SameValue(gold[k], v) {
			g.Passed, g.Detail = false, fmt.Sprintf("field %q is %v, gold %q", k, v, gold[k])
			return g, nil
		}
	}
	for _, k := range slices.Sorted(maps.Keys(got)) {
		if _, ok := gold[k]; !ok && given(got[k]) {
			g.Passed, g.Detail = false, fmt.Sprintf("field %q is %v, but the message gives no value for it", k, got[k])
			return g, nil
		}
	}
	return g, nil
}

// secondOpinion is which fields a second model's re-extraction left good, when one ran.
type secondOpinion struct {
	ran  bool
	good map[string]bool
}

// secondOpinion has the second model re-extract the fields the answer gave and merges its
// verdicts with the fields check's. A reply that is not JSON confirms nothing, so no field is good.
func (s *snips) secondOpinion(ctx context.Context, fields []string, text, answer string, got map[string]any,
	ask bench.Ask) (secondOpinion, error) {
	if ask == nil {
		return secondOpinion{}, nil
	}
	list := make([]checkgen.Field, len(fields))
	asked := false
	for i, f := range fields {
		list[i] = checkgen.Field{Name: f, Type: "string", FromSource: true, Required: given(got[f])}
		asked = asked || list[i].Required
	}
	out := secondOpinion{ran: true, good: map[string]bool{}}
	if !asked {
		return out, nil // the answer gave no field, so there is nothing to confirm or pay for
	}
	zero := 0.0
	rt, err := checkgen.JudgeRoundtrip(ctx, list, text, answer, func(ctx context.Context, prompt string) (string, error) {
		return ask(ctx, executor.Request{Prompt: prompt, Temperature: &zero, MaxTokens: snipsAnswerTokens})
	})
	if errors.Is(err, checkgen.ErrRoundtripReply) {
		return out, nil
	}
	if err != nil {
		return secondOpinion{}, err
	}
	for _, v := range checkgen.MergeVerdicts(checkgen.JudgeFields(list, text, answer), rt) {
		out.good[v.Name] = v.Good()
	}
	return out, nil
}

// tallyFields counts each gold field and each key the answer gave a value, by the kind
// of its gold value (or of the answer's, for a key with no gold): right when it matches
// the gold, flagged when the fields check, holding every value to the utterance, did not
// mark it good. A key outside the intent's fields is never checked, so always flagged.
func tallyFields(fields []string, text, answer string, gold map[string]string, got map[string]any,
	second secondOpinion) map[string]bench.FieldTally {
	list := make([]checkgen.Field, len(fields))
	for i, f := range fields {
		list[i] = checkgen.Field{Name: f, Type: "string", FromSource: true}
	}
	good := make(map[string]bool, len(fields))
	for _, v := range checkgen.JudgeFields(list, text, answer) {
		good[v.Name] = v.Good()
	}
	out := map[string]bench.FieldTally{}
	count := func(k, value string, right bool) {
		kind := bench.FieldKind(value)
		t, flagged := out[kind], !good[k]
		switch {
		case right && flagged:
			t.Right, t.RightFlagged = t.Right+1, t.RightFlagged+1
		case right:
			t.Right++
		case flagged:
			t.Wrong, t.WrongFlagged = t.Wrong+1, t.WrongFlagged+1
		default:
			t.Wrong++
		}
		if second.ran {
			// A field the second model did not confirm is not verified, whatever the source check said.
			t.Second = true
			if !second.good[k] {
				if right {
					t.RightFlaggedSecond++
				} else {
					t.WrongFlaggedSecond++
				}
			}
		}
		out[kind] = t
	}
	for k, v := range got {
		if _, isGold := gold[k]; !isGold && given(v) {
			count(k, fmt.Sprint(v), false)
		}
	}
	for k, want := range gold {
		count(k, want, got[k] != nil && checkgen.SameValue(want, got[k]))
	}
	return out
}

// given says the answer put a value in a key: null and "" leave it out.
func given(v any) bool { return v != nil && v != "" }

func (s *snips) fieldsOf(it bench.Item) ([]string, error) {
	if s.fields == nil {
		return nil, fmt.Errorf("suites: snips used before it loaded")
	}
	intent, _, _ := strings.Cut(it.ID, "/")
	f, ok := s.fields[intent]
	if !ok {
		return nil, fmt.Errorf("suites: snips item %s names no known intent", it.ID)
	}
	return f, nil
}
