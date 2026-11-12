// SPDX-License-Identifier: MIT

package rank

import (
	"math"
	"strings"

	"github.com/ankit373/hydra/internal/provider"
)

// Escalation picks, from heads, the one to ask once failed's answer was rejected:
// the weakest head measured in this class to beat failed, so the chain climbs and
// a declared score never decides. false when no measured head beats it.
func Escalation(heads []provider.Head, scores map[string]Score, failed provider.Head, asked []provider.Head) (int, bool) {
	bar := 0
	if sc := scores[failed.ID]; sc.Measured() {
		bar = sc.Effective
	}
	pick, fresh := -1, -1
	for i, h := range heads {
		if sc := scores[h.ID]; !sc.Measured() || sc.Effective <= bar {
			continue
		}
		if pick < 0 || climbLess(h, heads[pick], scores) {
			pick = i
		}
		if freshFamily(h, asked) && (fresh < 0 || climbLess(h, heads[fresh], scores)) {
			fresh = i
		}
	}
	// On a tie a family not yet asked is the better next voice: its errors are
	// less correlated with the answers already rejected.
	if fresh >= 0 && tied(scores[heads[fresh].ID], scores[heads[pick].ID]) {
		return fresh, true
	}
	return pick, pick >= 0
}

// climbLess is the smaller step up, then the cheaper, so a tie never pays more.
func climbLess(a, b provider.Head, scores map[string]Score) bool {
	sa, sb := scores[a.ID], scores[b.ID]
	if sa.Effective != sb.Effective {
		return sa.Effective < sb.Effective
	}
	if sa.CostUSD != sb.CostUSD {
		return sa.CostUSD < sb.CostUSD
	}
	return a.ID < b.ID
}

// tied reports two in-class Beta posteriors as indistinguishable: their means
// within 1.96 standard deviations of the difference, the 95% level that
// --confidence defaults to. Each posterior holds InDomain + MinCommitments.
func tied(a, b Score) bool {
	pa, pb := float64(a.Effective)/100, float64(b.Effective)/100
	va := pa * (1 - pa) / float64(a.InDomain+MinCommitments+1)
	vb := pb * (1 - pb) / float64(b.InDomain+MinCommitments+1)
	return math.Abs(pa-pb) <= 1.96*math.Sqrt(va+vb)
}

// family is the vendor behind a head: a LiteLLM head's upstream, else the vendor
// prefix of a "vendor/model" id, else the provider. No head carries a family field,
// so every model behind one local server reads as one family.
func family(h provider.Head) string {
	if up := h.Meta["litellm_upstream"]; up != "" {
		return up
	}
	if vendor, _, ok := strings.Cut(h.Meta["model"], "/"); ok && vendor != "" {
		return vendor
	}
	return h.Provider
}

func freshFamily(h provider.Head, asked []provider.Head) bool {
	for _, a := range asked {
		if family(a) == family(h) {
			return false
		}
	}
	return true
}
