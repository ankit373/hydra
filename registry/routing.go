// SPDX-License-Identifier: MIT

package registry

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// routing.yaml's first lines have always told operators that changing a value
// there updates every domain. Nothing read it: the map lived in a Go literal,
// so the on-disk override models.yaml and pricing.yaml honour did nothing for
// the one file named after routing (#720).

const (
	minEnumTier = 1
	maxEnumTier = 10
)

type routingFile struct {
	RoutingMap map[string]int `yaml:"routing_map"`
}

type routingResult struct {
	tiers map[string]int
	err   error
}

// Keyed by home rather than a package-level sync.Once, so a process that reads
// more than one registry root (tests, mainly) gets each one's real answer.
var routingCache struct {
	sync.Mutex
	byHome map[string]routingResult
}

// EnumTiers returns the routing enum to tier-number map, preferring home's
// override and falling back to the embedded copy.
//
// The returned map is a copy: it is handed to the router on every dispatch and
// a shared one would be one careless write away from retuning routing globally.
func EnumTiers(home string) (map[string]int, error) {
	routingCache.Lock()
	defer routingCache.Unlock()
	r, ok := routingCache.byHome[home]
	if !ok {
		r.tiers, r.err = loadEnumTiers(home)
		if routingCache.byHome == nil {
			routingCache.byHome = map[string]routingResult{}
		}
		routingCache.byHome[home] = r
	}
	return cloneTiers(r.tiers), r.err
}

// EnumKeys lists the enum keys weakest head first, which is the order a picker
// offers them in. Ties break alphabetically so the order is stable.
func EnumKeys(home string) ([]string, error) {
	tiers, err := EnumTiers(home)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(tiers))
	for k := range tiers {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if tiers[keys[i]] != tiers[keys[j]] {
			return tiers[keys[i]] > tiers[keys[j]]
		}
		return keys[i] < keys[j]
	})
	return keys, nil
}

func loadEnumTiers(home string) (map[string]int, error) {
	// The embedded copy defines which enums exist, so the check below is derived
	// from the shipped file rather than a second list in Go, which is the
	// duplication this change exists to remove.
	shipped, err := embedded.ReadFile("routing.yaml")
	if err != nil {
		return nil, fmt.Errorf("embedded routing.yaml is unreadable: %w", err)
	}
	base, err := parseRoutingMap(shipped)
	if err != nil {
		return nil, fmt.Errorf("embedded routing.yaml is unusable: %w", err)
	}

	// Read falls back to the embedded copy, so with no override this parses the
	// same bytes twice and the coverage check passes trivially. One path.
	raw, err := Read(home, "routing.yaml")
	if err != nil {
		return nil, fmt.Errorf("cannot read routing.yaml: %w", err)
	}
	got, err := parseRoutingMap(raw)
	if err != nil {
		return nil, err
	}
	if err := checkCoversBase(base, got); err != nil {
		return nil, err
	}
	return got, nil
}

func parseRoutingMap(raw []byte) (map[string]int, error) {
	var f routingFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("routing.yaml is not valid YAML: %w", err)
	}
	if len(f.RoutingMap) == 0 {
		return nil, errors.New("routing.yaml declares no routing_map")
	}
	for _, k := range sortedKeys(f.RoutingMap) {
		if v := f.RoutingMap[k]; v < minEnumTier || v > maxEnumTier {
			return nil, fmt.Errorf("routing.yaml: %s is tier %d, outside the valid range %d-%d",
				k, v, minEnumTier, maxEnumTier)
		}
	}
	return f.RoutingMap, nil
}

// checkCoversBase refuses a partial or inventive override. A missing key would
// route by the shipped rule with nothing to say so, and a stray one would mint
// a new enum out of a typo, which is exactly what IsKnownEnum exists to catch
// (#501).
func checkCoversBase(base, got map[string]int) error {
	var missing, extra []string
	for _, k := range sortedKeys(base) {
		if _, ok := got[k]; !ok {
			missing = append(missing, k)
		}
	}
	for _, k := range sortedKeys(got) {
		if _, ok := base[k]; !ok {
			extra = append(extra, k)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("routing.yaml override is missing %s; every enum must be listed, "+
			"a missing one would keep routing by the shipped rule with nothing to say so",
			strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		return fmt.Errorf("routing.yaml override declares unrecognized enum %s; recognized keys are %s",
			strings.Join(extra, ", "), strings.Join(sortedKeys(base), ", "))
	}
	return nil
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func cloneTiers(m map[string]int) map[string]int {
	if m == nil {
		return nil
	}
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
