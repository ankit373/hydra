// SPDX-License-Identifier: MIT

package suites

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"

	"github.com/ankit373/hydra/internal/bench"
)

// banking77 is PolyAI's Banking77: 77 fine-grained intents, ids by data row.
type banking77 struct{ labelSuite }

func (s *banking77) Rows(ds bench.Dataset, dataDir string) ([]bench.Row, error) {
	if err := ds.RequireFiles(s.name, "categories.json", "train.csv", "test.csv"); err != nil {
		return nil, err
	}
	raw, err := ds.Open(dataDir, "categories.json")
	if err != nil {
		return nil, err
	}
	var labels []string
	if err := json.Unmarshal(raw, &labels); err != nil {
		return nil, fmt.Errorf("suites: banking77 categories.json: %w", err)
	}
	if err := s.setLabels(labels); err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(labels))
	for _, l := range labels {
		known[l] = true
	}
	var rows []bench.Row
	for _, part := range []string{"train", "test"} {
		raw, err := ds.Open(dataDir, part+".csv")
		if err != nil {
			return nil, err
		}
		// encoding/csv, never line splitting: 13 utterances carry a quoted newline.
		recs, err := csv.NewReader(bytes.NewReader(raw)).ReadAll()
		if err != nil {
			return nil, fmt.Errorf("suites: banking77 %s.csv: %w", part, err)
		}
		if len(recs) == 0 || len(recs[0]) != 2 || recs[0][0] != "text" || recs[0][1] != "category" {
			return nil, fmt.Errorf("suites: banking77 %s.csv does not start with text,category", part)
		}
		recs = recs[1:]
		w := width(len(recs))
		for i, r := range recs {
			if !known[r[1]] {
				return nil, fmt.Errorf("suites: banking77 %s.csv row %d has label %q, not in categories.json", part, i, r[1])
			}
			rows = append(rows, bench.Row{ID: fmt.Sprintf("%s/%0*d", part, w, i), Text: r[0], Label: r[1]})
		}
	}
	return rows, nil
}
