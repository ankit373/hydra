// SPDX-License-Identifier: MIT

package dispatch

import (
	"sort"
	"strings"
)

// localAlias is the one --tier name that is not an enum key. It predates the
// enum vocabulary and names the free local floor, so it resolves through GRUNT
// rather than a hardcoded 10 and follows a routing.yaml override with it.
const (
	localAlias = "local"
	localEnum  = "GRUNT"
)

// ResolveTierName maps a --tier name to a tier number through routing.yaml,
// the same table --enum reads.
//
// Names used to resolve through cfg.Tiers, a CapScore-banded snapshot that
// `hyctl init` wrote once, so `--tier simple` and `--enum SIMPLE` picked
// different heads and the paid reading was the silent one (#782).
func ResolveTierName(name string) (int, bool) {
	tiers, err := enumTiers()
	if err != nil {
		return 0, false
	}
	key := strings.ToUpper(strings.TrimSpace(name))
	if key == strings.ToUpper(localAlias) {
		key = localEnum
	}
	n, ok := tiers[key]
	return n, ok
}

// TierNamesByTier groups the accepted --tier names by the tier they resolve to.
// It is the only place the name vocabulary is assembled, so a surface that
// lists the names and the router that resolves them cannot disagree.
func TierNamesByTier() map[int][]string {
	tiers, err := enumTiers()
	if err != nil {
		return nil
	}
	out := make(map[int][]string, len(tiers))
	for key, n := range tiers {
		out[n] = append(out[n], strings.ToLower(key))
	}
	if n, ok := tiers[localEnum]; ok {
		out[n] = append(out[n], localAlias)
	}
	for _, names := range out {
		sort.Strings(names)
	}
	return out
}

// TierNames lists what --tier accepts, weakest first, so the flag's help string
// and its "unknown tier" error are read off the table that resolves them and
// cannot drift from it the way the old fixed list did.
func TierNames() []string {
	byTier := TierNamesByTier()
	tiers := make([]int, 0, len(byTier))
	for t := range byTier {
		tiers = append(tiers, t)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(tiers)))

	var out []string
	for _, t := range tiers {
		out = append(out, byTier[t]...)
	}
	return out
}
