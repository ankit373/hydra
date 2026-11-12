// SPDX-License-Identifier: MIT

package suites

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ankit373/hydra/internal/bench"
)

// SmokeTestItems is the smoke tier's test sample per suite, listed in smoke.ids.
const SmokeTestItems = 100

// GenerateSplits writes bench/splits/<suite>.tsv and bench/configs/smoke.ids from
// the fetched data. Run once by `go generate`, never at run time; it returns how
// many items each suite excluded as conflicting duplicates.
func GenerateSplits(lock map[string]bench.Dataset, dataRoot, benchDir string) (map[string]int, error) {
	excluded := map[string]int{}
	var smoke bytes.Buffer
	for _, name := range bench.Names(lock) {
		mk, ok := Registry()[name]
		if !ok {
			return nil, fmt.Errorf("suites: the lockfile pins %s, which no suite loads", name)
		}
		s := mk()
		rows, err := s.Rows(lock[name], bench.DataDir(dataRoot, name))
		if err != nil {
			return nil, err
		}
		split, err := bench.AssignSplits(name, rows)
		if err != nil {
			return nil, err
		}
		var tsv bytes.Buffer
		if err := bench.WriteSplits(&tsv, split); err != nil {
			return nil, err
		}
		if err := writeFile(filepath.Join(benchDir, "splits", name+".tsv"), tsv.Bytes()); err != nil {
			return nil, err
		}
		for _, it := range bench.FirstByRank(SplitItems(split), bench.SplitTest, SmokeTestItems) {
			fmt.Fprintf(&smoke, "%s\t%s\n", name, it.ID)
		}
		for _, r := range split {
			if r.Split == bench.SplitExcluded {
				excluded[name]++
			}
		}
	}
	return excluded, writeFile(filepath.Join(benchDir, "configs", "smoke.ids"), smoke.Bytes())
}

// SplitItems lifts TSV rows into Items carrying only id, split and rank, which is
// all rank selection reads.
func SplitItems(rows []bench.SplitRow) []bench.Item {
	out := make([]bench.Item, len(rows))
	for i, r := range rows {
		out[i] = bench.Item{ID: r.ID, Split: r.Split, Rank: r.Rank}
	}
	return out
}

func writeFile(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
