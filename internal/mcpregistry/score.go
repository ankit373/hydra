// SPDX-License-Identifier: MIT

package mcpregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Confidence states how much evidence backs a score. "Insufficient" is a
// distinct third state from low/moderate/high — a server with no evidence
// yet must never render the same as one found to be safe or unsafe (§10.2
// of the design doc: this is a design requirement, not a rounding choice).
type Confidence string

const (
	ConfidenceInsufficient Confidence = "insufficient_evidence"
	ConfidenceLow          Confidence = "low"
	ConfidenceModerate     Confidence = "moderate"
	ConfidenceHigh         Confidence = "high"
)

// Signal is one piece of evidence behind a score. Always shown alongside the
// number it feeds — a bare score with no signals is exactly what every
// existing MCP directory already gets wrong (star/download counts with no
// explanation). Impact ranges -100..100; Available false means the signal
// couldn't be evaluated (network failure, unsupported ecosystem, non-GitHub
// repo) rather than "evaluated and neutral".
type Signal struct {
	Name      string  `json:"name"`
	Detail    string  `json:"detail"`
	Impact    float64 `json:"impact"`
	Available bool    `json:"available"`
}

// CategoryScore is one of the CSA MCP Selection Scorecard's four categories
// (modelcontextprotocol-security.io/audit/scorecard.html) — Hydra automates
// signal collection into a taxonomy the MCP Security Working Group already
// endorsed, rather than inventing a competing one.
type CategoryScore struct {
	Value      float64    `json:"value"` // 0-100, meaningless if Confidence is insufficient
	Confidence Confidence `json:"confidence"`
	Signals    []Signal   `json:"signals"`
}

// Score is the Phase 2 trust score for one resolved server.
type Score struct {
	SecurityImplementation CategoryScore `json:"security_implementation"` // weight 0.35
	RepositoryHealth       CategoryScore `json:"repository_health"`       // weight 0.25
	OperationalSecurity    CategoryScore `json:"operational_security"`    // weight 0.25
	CommunityGovernance    CategoryScore `json:"community_governance"`    // weight 0.15
	Overall                float64       `json:"overall"`
	Confidence             Confidence    `json:"confidence"`
}

const (
	weightSecurityImplementation = 0.35
	weightRepositoryHealth       = 0.25
	weightOperationalSecurity    = 0.25
	weightCommunityGovernance    = 0.15
)

// ComputeScore scores one resolved registry server. corpus is the full
// synced dataset, used for the typosquat/near-duplicate check. A server with
// no computable signals in a category returns that category at
// ConfidenceInsufficient rather than a default-neutral number.
func ComputeScore(ctx context.Context, srv ServerRecord, corpus []ServerRecord) Score {
	var secSignals, repoSignals, opSignals, commSignals []Signal

	for _, pkg := range srv.Packages {
		secSignals = append(secSignals, knownBadSignal(ctx, pkg))
	}
	secSignals = append(secSignals, typosquatSignal(srv, corpus))

	repoSignals = append(repoSignals, maintenanceSignal(ctx, srv.Repository.URL))

	opSignals = append(opSignals, declaredAuthSignal(srv))

	commSignals = append(commSignals, registryPresenceSignal())

	sec := aggregate(secSignals)
	repo := aggregate(repoSignals)
	op := aggregate(opSignals)
	comm := aggregate(commSignals)

	overall := 0.0
	weightSum := 0.0
	for _, cs := range []struct {
		score  CategoryScore
		weight float64
	}{
		{sec, weightSecurityImplementation},
		{repo, weightRepositoryHealth},
		{op, weightOperationalSecurity},
		{comm, weightCommunityGovernance},
	} {
		if cs.score.Confidence == ConfidenceInsufficient {
			continue
		}
		overall += cs.score.Value * cs.weight
		weightSum += cs.weight
	}
	if weightSum > 0 {
		overall /= weightSum
	}

	return Score{
		SecurityImplementation: sec,
		RepositoryHealth:       repo,
		OperationalSecurity:    op,
		CommunityGovernance:    comm,
		Overall:                overall,
		Confidence:             overallConfidence(weightSum),
	}
}

// aggregate turns a category's signals into a 0-100 value plus a confidence
// derived from how many of the signals were actually available — the
// "how many of the signals were computable" measure the design doc uses in
// place of a borrowed calibration engine with no training data (§12/§13).
func aggregate(signals []Signal) CategoryScore {
	base := 70.0 // neutral starting point: no evidence either way
	total := 0.0
	available := 0
	for _, s := range signals {
		if s.Available {
			total += s.Impact
			available++
		}
	}
	if available == 0 {
		return CategoryScore{Confidence: ConfidenceInsufficient, Signals: signals}
	}

	value := base + total
	if value < 0 {
		value = 0
	}
	if value > 100 {
		value = 100
	}

	conf := ConfidenceLow
	switch {
	case available >= len(signals) && len(signals) >= 2:
		conf = ConfidenceHigh
	case float64(available) >= float64(len(signals))*0.5:
		conf = ConfidenceModerate
	}
	return CategoryScore{Value: value, Confidence: conf, Signals: signals}
}

func overallConfidence(weightSum float64) Confidence {
	switch {
	case weightSum <= 0:
		return ConfidenceInsufficient
	case weightSum < 0.5:
		return ConfidenceLow
	case weightSum < 0.85:
		return ConfidenceModerate
	default:
		return ConfidenceHigh
	}
}

// ── Security Implementation signals ─────────────────────────────────────

var osvEcosystem = map[string]string{
	"npm":    "npm",
	"pypi":   "PyPI",
	"docker": "", // OSV has no generic Docker-image ecosystem; skip, Available=false
}

var osvQueryURL = "https://api.osv.dev/v1/query"

type osvVuln struct {
	ID       string `json:"id"`
	Summary  string `json:"summary"`
	Severity []struct {
		Score string `json:"score"`
	} `json:"database_specific,omitempty"`
}

type osvResponse struct {
	Vulns []osvVuln `json:"vulns"`
}

// knownBadSignal cross-references a package against OSV.dev's advisory
// database — the highest-value signal per the design doc's evidence: OX
// Security found 9 of 11 registries accept a known-malicious clone with zero
// review. This is the check that would have caught it.
func knownBadSignal(ctx context.Context, pkg Package) Signal {
	eco, ok := osvEcosystem[pkg.RegistryType]
	if !ok || eco == "" || pkg.Identifier == "" {
		return Signal{Name: "known-vulnerability match", Available: false}
	}

	body, err := json.Marshal(map[string]any{
		"package": map[string]string{"name": pkg.Identifier, "ecosystem": eco},
	})
	if err != nil {
		return Signal{Name: "known-vulnerability match", Available: false}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, osvQueryURL, strings.NewReader(string(body)))
	if err != nil {
		return Signal{Name: "known-vulnerability match", Available: false}
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return Signal{Name: "known-vulnerability match", Available: false}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Signal{Name: "known-vulnerability match", Available: false}
	}

	var out osvResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Signal{Name: "known-vulnerability match", Available: false}
	}
	if len(out.Vulns) == 0 {
		return Signal{Name: "known-vulnerability match", Detail: "no known advisories", Impact: 0, Available: true}
	}
	return Signal{
		Name:      "known-vulnerability match",
		Detail:    fmt.Sprintf("%d known advisory/advisories, e.g. %s", len(out.Vulns), out.Vulns[0].ID),
		Impact:    -100,
		Available: true,
	}
}

// typosquatSignal flags a package identifier that's suspiciously close (edit
// distance <=2) to a different, already-registered identifier — the exact
// pattern OX Security proved works against real registries. There's no
// popularity signal in the official registry's schema to compare against
// (deliberately: §7 found popularity is actively misleading), so this
// compares against the whole synced corpus rather than a "popular names"
// list.
func typosquatSignal(srv ServerRecord, corpus []ServerRecord) Signal {
	for _, pkg := range srv.Packages {
		if pkg.Identifier == "" {
			continue
		}
		for _, other := range corpus {
			if other.Name == srv.Name {
				continue
			}
			for _, otherPkg := range other.Packages {
				if otherPkg.Identifier == "" || otherPkg.Identifier == pkg.Identifier {
					continue
				}
				d := levenshtein(pkg.Identifier, otherPkg.Identifier)
				if d > 0 && d <= 2 && d < len(pkg.Identifier)/3+1 {
					return Signal{
						Name:      "near-duplicate identifier",
						Detail:    fmt.Sprintf("%q is %d edit(s) from %q (published by a different entry) — verify this isn't a typosquat", pkg.Identifier, d, otherPkg.Identifier),
						Impact:    -40,
						Available: true,
					}
				}
			}
		}
	}
	return Signal{Name: "near-duplicate identifier", Detail: "no close match found", Impact: 0, Available: true}
}

// NearestIdentifier finds the closest package identifier in corpus to
// candidate, for flagging an unresolved installed server that looks like a
// near-miss of something in the registry rather than something wholly
// unknown. Returns ("", -1) if corpus has no package identifiers at all.
func NearestIdentifier(candidate string, corpus []ServerRecord) (string, int) {
	best := ""
	bestDist := -1
	for _, srv := range corpus {
		for _, pkg := range srv.Packages {
			if pkg.Identifier == "" {
				continue
			}
			d := levenshtein(candidate, pkg.Identifier)
			if bestDist == -1 || d < bestDist {
				bestDist = d
				best = pkg.Identifier
			}
		}
	}
	return best, bestDist
}

// levenshtein computes edit distance with O(min(len(a),len(b))) space.
func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	ra, rb := []rune(a), []rune(b)
	if len(ra) > len(rb) {
		ra, rb = rb, ra
	}
	prev := make([]int, len(ra)+1)
	for i := range prev {
		prev[i] = i
	}
	curr := make([]int, len(ra)+1)
	for j := 1; j <= len(rb); j++ {
		curr[0] = j
		for i := 1; i <= len(ra); i++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[i] = min3(curr[i-1]+1, prev[i]+1, prev[i-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(ra)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

// ── Repository Health signals ────────────────────────────────────────────

var githubAPIBase = "https://api.github.com"

type githubRepo struct {
	PushedAt  time.Time `json:"pushed_at"`
	Archived  bool      `json:"archived"`
	CreatedAt time.Time `json:"created_at"`
}

// maintenanceRecencyThreshold: no push in this long is treated as elevated
// abandonment risk. Not a precise hazard-rate model (that needs contributor
// counts and release cadence GitHub's single repo GET doesn't cheaply
// provide — see design doc §12 for the Cox proportional-hazards research
// this is informed by), but the same directional signal: staleness is real
// and current popularity signals miss it.
const maintenanceRecencyThreshold = 180 * 24 * time.Hour

// maintenanceSignal fetches the repo's last-push date from GitHub. Best
// effort: a non-GitHub repository, a rate-limited request, or any fetch
// error all degrade to Available=false rather than failing the whole score.
func maintenanceSignal(ctx context.Context, repoURL string) Signal {
	owner, repo, ok := parseGitHubURL(repoURL)
	if !ok {
		return Signal{Name: "maintenance recency", Available: false}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/repos/%s/%s", githubAPIBase, owner, repo), nil)
	if err != nil {
		return Signal{Name: "maintenance recency", Available: false}
	}
	req.Header.Set("User-Agent", "hydra/1 (+https://github.com/ankit373/hydra)")
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return Signal{Name: "maintenance recency", Available: false}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0" {
		return Signal{Name: "maintenance recency", Detail: "GitHub API rate-limited", Available: false}
	}
	if resp.StatusCode != http.StatusOK {
		return Signal{Name: "maintenance recency", Available: false}
	}

	var gh githubRepo
	if err := json.NewDecoder(resp.Body).Decode(&gh); err != nil {
		return Signal{Name: "maintenance recency", Available: false}
	}

	if gh.Archived {
		return Signal{Name: "maintenance recency", Detail: "repository is archived", Impact: -60, Available: true}
	}

	age := time.Since(gh.PushedAt)
	if age > maintenanceRecencyThreshold {
		days := int(age.Hours() / 24)
		return Signal{
			Name:      "maintenance recency",
			Detail:    fmt.Sprintf("no push in %d days (threshold %d)", days, int(maintenanceRecencyThreshold.Hours()/24)),
			Impact:    -30,
			Available: true,
		}
	}
	return Signal{
		Name:      "maintenance recency",
		Detail:    fmt.Sprintf("last push %s ago", age.Round(24*time.Hour)),
		Impact:    0,
		Available: true,
	}
}

func parseGitHubURL(url string) (owner, repo string, ok bool) {
	const prefix = "github.com/"
	i := strings.Index(url, prefix)
	if i == -1 {
		return "", "", false
	}
	rest := strings.TrimSuffix(url[i+len(prefix):], ".git")
	rest = strings.Trim(rest, "/")
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// ── Operational Security signals ─────────────────────────────────────────

// declaredAuthSignal checks whether a server exposing a remote (URL-based)
// endpoint declares an auth header for it. Explicitly labeled "declared, not
// verified" per the design doc's Goodhart's-law correction (§13.2): a
// malicious server can trivially declare a header it doesn't enforce, so
// this is weighted lower than an independently-checkable signal like a
// known-vulnerability match, never presented as a verified guarantee.
//
// Only applies to remote servers — a stdio server runs locally under the
// user's own OS permissions, so "remote auth posture" isn't a meaningful
// question for it, and this correctly reports Available=false rather than
// inventing a score for a question that doesn't apply.
func declaredAuthSignal(srv ServerRecord) Signal {
	if len(srv.Remotes) == 0 {
		return Signal{Name: "declared remote auth (not independently verified)", Available: false}
	}

	for _, r := range srv.Remotes {
		for _, h := range r.Headers {
			if h.IsSecret {
				return Signal{
					Name:      "declared remote auth (not independently verified)",
					Detail:    fmt.Sprintf("declares a credential header (%s) for its remote endpoint", h.Name),
					Impact:    10,
					Available: true,
				}
			}
		}
	}
	return Signal{
		Name:      "declared remote auth (not independently verified)",
		Detail:    "remote endpoint declares no auth header — matches the pattern security research found in the large majority of exposed production MCP servers",
		Impact:    -30,
		Available: true,
	}
}

// ── Community & Governance signals ───────────────────────────────────────

// registryPresenceSignal is the baseline: this function is only called for
// servers already resolved against the namespace-verified official
// registry (Phase 1's StatusVerified), which is itself a weak-but-real
// positive per §2 (identity is verified, safety is not).
func registryPresenceSignal() Signal {
	return Signal{Name: "namespace-verified registry presence", Detail: "publisher identity verified by the official registry", Impact: 10, Available: true}
}

// FormatConfidence renders a Confidence for CLI output.
func FormatConfidence(c Confidence) string {
	return strings.ReplaceAll(string(c), "_", " ")
}

// FormatScore renders an overall score as a rounded percentage string, or
// "insufficient evidence" — never a bare number for a score with no signal.
func FormatScore(s Score) string {
	if s.Confidence == ConfidenceInsufficient {
		return "insufficient evidence"
	}
	return strconv.Itoa(int(s.Overall+0.5)) + "/100"
}
