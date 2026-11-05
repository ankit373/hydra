// SPDX-License-Identifier: MIT

package bench

import (
	"fmt"

	"github.com/ankit373/hydra/internal/build"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/provider"
)

// Plan freezes one suite's run: the first nTest test items by rank, the arms named
// (all of the config's when none are), their models' prices, and the breadcrumb.
func Plan(c *Config, s Suite, ds Dataset, splitsPath string, all []Item, nTest int, arms []string,
	prices PriceSnapshot, host HostInfo) (Run, map[string]Item, error) {
	cfgs, err := c.ArmConfigs()
	if err != nil {
		return Run{}, nil, err
	}
	order := c.Arms
	if len(arms) > 0 {
		order = nil
		for _, a := range arms {
			if _, ok := cfgs[a]; !ok {
				return Run{}, nil, fmt.Errorf("bench: arm %s is not in the config", a)
			}
			order = append(order, a)
		}
	}
	chosen := map[string]ArmConfig{}
	used := PriceSnapshot{}
	cutoffs := map[string]string{}
	for _, a := range order {
		chosen[a] = cfgs[a]
		for _, spec := range cfgs[a].Models {
			_, m, _ := ParseModel(spec) // ArmConfigs parsed every spec already
			p, ok := prices[m]
			if !ok {
				return Run{}, nil, fmt.Errorf("bench: %s has no price in the snapshot", m)
			}
			used[m], cutoffs[m] = p, c.Cutoffs[m]
		}
	}
	tests := FirstByRank(all, SplitTest, nTest)
	if len(tests) == 0 {
		return Run{}, nil, fmt.Errorf("bench: %s has no test items", s.Name())
	}
	req, err := s.Prompt(tests[0])
	if err != nil || req.Temperature == nil {
		return Run{}, nil, fmt.Errorf("bench: %s does not pin a temperature: %v", s.Name(), err)
	}
	byID := make(map[string]Item, len(tests))
	ids := make([]string, len(tests))
	for i, it := range tests {
		byID[it.ID], ids[i] = it, it.ID
	}
	split, err := FileSHA256(splitsPath)
	if err != nil {
		return Run{}, nil, err
	}
	sha, dirty := buildInfo()
	if build.Commit != "none" || sha == "" {
		sha = build.Commit
	}
	run := Run{Track: c.Track, Suite: s.Name(), Tier: c.Tier, Seed: c.Seed, Arms: chosen, ArmOrder: order,
		ItemIDs: ids, TestSplitSize: len(FirstByRank(all, SplitTest, len(all))), Prices: used}
	run.Crumb = Breadcrumb{HarnessVersion: build.Version, GitSHA: sha, Dirty: dirty, Suite: s.Name(),
		SuiteRevision: ds.Revision, Licence: ds.Licence, LicenceVerified: ds.LicenceVerified, DataSHA256: ds.Hashes(),
		SplitSHA256: split, Seed: c.Seed, Arms: chosen,
		Prices: used, Temperature: *req.Temperature, Host: host, ModelCutoffs: cutoffs}
	return run, byID, nil
}

// BuildArms makes each arm in run from its frozen config. Only static-fallback carries
// a fault plan, keyed on the run's seed.
func BuildArms(run Run, exec func(provider.Head) executor.Executor) (map[string]Arm, error) {
	out := map[string]Arm{}
	for name, ac := range run.Arms {
		var faults *FaultPlan
		if ac.FaultRate > 0 {
			faults = &FaultPlan{Rate: ac.FaultRate, Seed: run.Seed}
		}
		d, err := NewDirect(name, ac, run.Prices, exec, faults)
		if err != nil {
			return nil, err
		}
		out[name] = d
	}
	return out, nil
}
