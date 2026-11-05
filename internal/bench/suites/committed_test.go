// SPDX-License-Identifier: MIT

package suites

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/bench"
)

const benchDir = "../../../bench"

func committedSplits(t *testing.T, suite string) []bench.SplitRow {
	t.Helper()
	f, err := os.Open(filepath.Join(benchDir, "splits", suite+".tsv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := bench.ReadSplits(f)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// The parsed counts match upstream, and every rank is recomputable from its id, on
// any platform, without the data.
func TestCommittedSplits_CountsAndRanks(t *testing.T) {
	for suite, want := range map[string]struct{ rows, excluded int }{
		"banking77": {13083, 0},
		"clinc150":  {23700, 8},
	} {
		rows := committedSplits(t, suite)
		ids := map[string]bool{}
		excluded := 0
		for _, r := range rows {
			if ids[r.ID] {
				t.Fatalf("%s: id %s appears twice, so it can sit in both splits", suite, r.ID)
			}
			ids[r.ID] = true
			if r.Rank != bench.Rank(suite, r.ID) {
				t.Fatalf("%s: %s has rank %d, the hash gives %d", suite, r.ID, r.Rank, bench.Rank(suite, r.ID))
			}
			if r.Split == bench.SplitExcluded {
				excluded++
			}
		}
		if len(rows) != want.rows || excluded != want.excluded {
			t.Errorf("%s: %d rows, %d excluded; want %d and %d", suite, len(rows), excluded, want.rows, want.excluded)
		}
	}
}

// Twenty named ids per suite keep their split and rank: regenerating the TSVs with a
// changed key or normaliser shows up here rather than as a silently new sample.
func TestCommittedSplits_GoldenIDs(t *testing.T) {
	golden := map[string][]struct {
		id    string
		split bench.Split
		rank  uint64
	}{
		"banking77": {
			{"train/00000", bench.SplitTest, 17139974644568928149},
			{"train/00654", bench.SplitTest, 12211928876454602921},
			{"train/01308", bench.SplitTest, 9407973136943775828},
			{"train/01962", bench.SplitCalibration, 8215607510112039089},
			{"train/02616", bench.SplitCalibration, 2108314678784885910},
			{"train/03270", bench.SplitCalibration, 5795625422324848347},
			{"train/03924", bench.SplitTest, 7503879245371965642},
			{"train/04578", bench.SplitTest, 12841274677075611330},
			{"train/05232", bench.SplitCalibration, 13439084608651764946},
			{"train/05886", bench.SplitCalibration, 7631040026851803370},
			{"train/06540", bench.SplitTest, 1533994957865195846},
			{"train/07194", bench.SplitTest, 12462926340447255712},
			{"train/07848", bench.SplitTest, 7387162475213015418},
			{"train/08502", bench.SplitTest, 1832449647760624692},
			{"train/09156", bench.SplitTest, 10545197088080018599},
			{"train/09810", bench.SplitTest, 18025322600142619722},
			{"test/0461", bench.SplitTest, 10316525231214393304},
			{"test/1115", bench.SplitTest, 11714373277330524037},
			{"test/1769", bench.SplitTest, 6233068111461457431},
			{"test/2423", bench.SplitTest, 14244322241115625813},
		},
		"clinc150": {
			{"train/00000", bench.SplitTest, 18057128959040240185},
			{"train/01185", bench.SplitTest, 735837854616360676},
			{"train/02370", bench.SplitTest, 13402132073593027664},
			{"train/03555", bench.SplitTest, 1491598065976684272},
			{"train/04740", bench.SplitCalibration, 3718970751045372919},
			{"train/05925", bench.SplitTest, 5634756140351000085},
			{"train/07110", bench.SplitCalibration, 7448335442286423450},
			{"train/07424", bench.SplitExcluded, 8013249842510791036},
			{"train/08295", bench.SplitTest, 1672804787119117089},
			{"train/10665", bench.SplitCalibration, 6681437942570923231},
			{"train/11130", bench.SplitExcluded, 13843740107893634536},
			{"train/12066", bench.SplitExcluded, 1327804187450184339},
			{"train/13035", bench.SplitCalibration, 1074117949514226161},
			{"train/14018", bench.SplitExcluded, 18112618693343961428},
			{"val/0405", bench.SplitCalibration, 7912377470372699824},
			{"val/1011", bench.SplitExcluded, 5396087473180448994},
			{"val/1590", bench.SplitTest, 5324412015881342908},
			{"val/1794", bench.SplitExcluded, 14432330197450269074},
			{"test/0599", bench.SplitExcluded, 17927560966528348799},
			{"test/0938", bench.SplitExcluded, 1639612101800119413},
		},
	}
	for suite, want := range golden {
		by := map[string]bench.SplitRow{}
		for _, r := range committedSplits(t, suite) {
			by[r.ID] = r
		}
		for _, g := range want {
			if r := by[g.id]; r.Split != g.split || r.Rank != g.rank {
				t.Errorf("%s %s = (%v, %d), want (%v, %d)", suite, g.id, r.Split, r.Rank, g.split, g.rank)
			}
		}
	}
}

// smoke.ids is the first SmokeTestItems test ids by rank, so a reviewer reads the
// exact sample and the tiers nest.
func TestCommittedSmokeIDs_AreTheFirstTestItemsByRank(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(benchDir, "configs", "smoke.ids"))
	if err != nil {
		t.Fatal(err)
	}
	var want strings.Builder
	for _, suite := range []string{"banking77", "clinc150"} {
		for _, it := range bench.FirstByRank(SplitItems(committedSplits(t, suite)), bench.SplitTest, SmokeTestItems) {
			want.WriteString(suite + "\t" + it.ID + "\n")
		}
	}
	if string(b) != want.String() {
		t.Fatal("bench/configs/smoke.ids is not the first 100 test ids by rank per suite; rerun go generate")
	}
}

// The committed lockfile parses and pins exactly the files each loader reads.
func TestCommittedLock_PinsWhatTheLoadersRead(t *testing.T) {
	lock, err := bench.ReadLock(filepath.Join(benchDir, "datasets.lock"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"banking77": {"categories.json", "train.csv", "test.csv"}, "clinc150": {"data_full.json"}}
	if len(lock) != len(Registry()) {
		t.Fatalf("lock pins %v, registry has %d suites", bench.Names(lock), len(Registry()))
	}
	for suite, files := range want {
		if err := lock[suite].RequireFiles(suite, files...); err != nil {
			t.Error(err)
		}
		if lock[suite].LicenceVerified == "" {
			t.Errorf("%s: licence not marked verified", suite)
		}
	}
}
