// SPDX-License-Identifier: MIT

package suites

import (
	"context"
	"encoding/json"
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
func (s *snips) Grade(_ context.Context, it bench.Item, answer string) (bench.Grade, error) {
	if _, err := s.fieldsOf(it); err != nil {
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
	for _, k := range slices.Sorted(maps.Keys(gold)) {
		v, ok := got[k]
		if !ok || v == nil {
			return bench.Grade{Detail: fmt.Sprintf("field %q is missing, gold %q", k, gold[k])}, nil
		}
		if !checkgen.SameValue(gold[k], v) {
			return bench.Grade{Detail: fmt.Sprintf("field %q is %v, gold %q", k, v, gold[k])}, nil
		}
	}
	for _, k := range slices.Sorted(maps.Keys(got)) {
		if _, ok := gold[k]; !ok && got[k] != nil && got[k] != "" {
			return bench.Grade{Detail: fmt.Sprintf("field %q is %v, but the message gives no value for it", k, got[k])}, nil
		}
	}
	return bench.Grade{Passed: true}, nil
}

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
