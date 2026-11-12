// SPDX-License-Identifier: MIT

package suites

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/ankit373/hydra/internal/bench"
)

// clincParts are data_full.json's keys, in the order ids are assigned.
var clincParts = []string{"train", "val", "test", "oos_train", "oos_val", "oos_test"}

// clinc150 is Larson et al.'s CLINC150: 150 intents plus `oos`, an ordinary label here.
type clinc150 struct{ labelSuite }

func (s *clinc150) Rows(ds bench.Dataset, dataDir string) ([]bench.Row, error) {
	if err := ds.RequireFiles(s.name, "data_full.json"); err != nil {
		return nil, err
	}
	raw, err := ds.Open(dataDir, "data_full.json")
	if err != nil {
		return nil, err
	}
	var data map[string][][2]string
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("suites: clinc150 data_full.json: %w", err)
	}
	if len(data) != len(clincParts) {
		return nil, fmt.Errorf("suites: clinc150 data_full.json has %d parts, want %v", len(data), clincParts)
	}
	var rows []bench.Row
	seen := map[string]bool{}
	for _, part := range clincParts {
		recs, ok := data[part]
		if !ok {
			return nil, fmt.Errorf("suites: clinc150 data_full.json has no %q", part)
		}
		w := width(len(recs))
		for i, r := range recs {
			rows = append(rows, bench.Row{ID: fmt.Sprintf("%s/%0*d", part, w, i), Text: r[0], Label: r[1]})
			seen[r[1]] = true
		}
	}
	labels := make([]string, 0, len(seen))
	for l := range seen {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	return rows, s.setLabels(labels)
}
