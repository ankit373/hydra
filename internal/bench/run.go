// SPDX-License-Identifier: MIT

package bench

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"runtime/debug"
	"strconv"
)

// Run is the frozen description of one run, written to run.json before the first
// call. Its inputs hash to the id's suffix, so the same command resumes the same run.
type Run struct {
	ID            string               `json:"id"` // "<track>-<suite>-<tier>-s<seed>-<cfg8>[-r<k>]"
	Track         string               `json:"track"`
	Suite         string               `json:"suite"`
	Tier          string               `json:"tier"`
	Seed          uint64               `json:"seed"`
	MaxUSDHistory []float64            `json:"max_usd_history"` // every cap the run was given, oldest first
	Arms          map[string]ArmConfig `json:"arms"`
	ArmOrder      []string             `json:"arm_order"`
	ItemIDs       []string             `json:"item_ids"`        // the scheduled test items, in rank order
	TestSplitSize int                  `json:"test_split_size"` // the ceiling on any recommended n
	Prices        PriceSnapshot        `json:"prices"`
	Crumb         Breadcrumb           `json:"breadcrumb"`
}

// Breadcrumb is what a reader needs to reproduce or distrust a number.
type Breadcrumb struct {
	HarnessVersion string `json:"harness_version"`
	GitSHA         string `json:"git_sha"`
	Dirty          bool   `json:"dirty"`
	Config         string `json:"config"` // config.Breadcrumb() of each served home; "" for direct-only
	Suite          string `json:"suite"`
	SuiteRevision  string `json:"suite_revision"`
	Licence        string `json:"licence"`
	// LicenceVerified is empty when nobody checked; `hyctl bench table` will not publish such a suite.
	LicenceVerified string               `json:"licence_verified,omitempty"`
	DataSHA256      map[string]string    `json:"data_sha256"` // re-hashed at load
	SplitSHA256     string               `json:"split_sha256"`
	Seed            uint64               `json:"seed"`
	Arms            map[string]ArmConfig `json:"arms"`
	Prices          PriceSnapshot        `json:"prices"`
	ProviderModels  map[string][]string  `json:"provider_models,omitempty"` // per arm, read off the rows by report
	Temperature     float64              `json:"temperature"`               // the value sent
	CalibSHA256     string               `json:"calib_sha256,omitempty"`
	Host            HostInfo             `json:"host"`
	ModelCutoffs    map[string]string    `json:"model_cutoffs"` // stated training cutoffs, for contamination
}

// HostInfo is the machine at the start of the run; latency on a loaded one is noise.
type HostInfo struct {
	OS          string  `json:"os"`
	Arch        string  `json:"arch"`
	CPUs        int     `json:"cpus"`
	TotalRAMGB  float64 `json:"total_ram_gb"`
	FreeRAMGB   float64 `json:"free_ram_gb"`
	MemPressure string  `json:"mem_pressure"`
}

// cfg8 hashes the run's inputs. max_usd is left out, so raising the cap resumes the
// same run; so is a price's fetch time, so a cache refresh that changed no price does too.
func (r *Run) cfg8() string {
	type price struct {
		Model, Ref, Source string
		In, Out            float64
	}
	prices := map[string]price{}
	for k, p := range r.Prices {
		prices[k] = price{p.Model, p.Ref, p.Source, p.InPerM, p.OutPerM}
	}
	b, _ := json.Marshal(struct {
		Track, Suite, Tier string
		Seed               uint64
		Arms               map[string]ArmConfig
		Order, Items       []string
		Prices             map[string]price
		Split              string
		Data               map[string]string
	}{r.Track, r.Suite, r.Tier, r.Seed, r.Arms, r.ArmOrder, r.ItemIDs, prices, r.Crumb.SplitSHA256, r.Crumb.DataSHA256})
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:4])
}

// baseID is the run id without a rerun suffix.
func (r *Run) baseID() string {
	return fmt.Sprintf("%s-%s-%s-s%d-%s", r.Track, r.Suite, r.Tier, r.Seed, r.cfg8())
}

// pair is one scheduled (item, arm) call.
type pair struct {
	Seq  int
	Item Item
	Arm  string
}

// schedule shuffles the items on the seed alone and rotates the arm order per item,
// ABAB, so provider drift hits every arm evenly. The seed never picks the items.
func schedule(items []Item, arms []string, seed uint64) []pair {
	order := append([]Item(nil), items...)
	rng := rand.New(rand.NewPCG(seed, 0xB5AD4ECEDA1CE2A9))
	for i := len(order) - 1; i > 0; i-- {
		j := rng.IntN(i + 1)
		order[i], order[j] = order[j], order[i]
	}
	var out []pair
	for j, it := range order {
		for k := range arms {
			out = append(out, pair{Seq: len(out), Item: it, Arm: arms[(j+k)%len(arms)]})
		}
	}
	return out
}

// buildInfo reads the vcs stamp Go embeds, so a dirty tree is recorded as dirty.
func buildInfo() (sha string, dirty bool) {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			sha = s.Value
		case "vcs.modified":
			dirty, _ = strconv.ParseBool(s.Value)
		}
	}
	return sha, dirty
}
