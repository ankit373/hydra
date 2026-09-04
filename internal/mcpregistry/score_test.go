// SPDX-License-Identifier: MIT

package mcpregistry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLevenshtein(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"", "abc", 3},
		{"kitten", "sitting", 3},
		{"mcp-server-postgres", "mcp-server-postgress", 1},
		{"chrome-mcp", "chrome-devtools-mcp", 9},
	}
	for _, tt := range tests {
		if got := levenshtein(tt.a, tt.b); got != tt.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestTyposquatSignal_FlagsCloseIdentifier(t *testing.T) {
	target := ServerRecord{Name: "a", Packages: []Package{{Identifier: "mcp-server-postgres"}}}
	corpus := []ServerRecord{
		target,
		{Name: "b", Packages: []Package{{Identifier: "mcp-server-postgress"}}}, // 1 edit away, different entry
	}
	sig := typosquatSignal(target, corpus)
	if !sig.Available || sig.Impact >= 0 {
		t.Fatalf("expected a negative-impact typosquat flag, got %+v", sig)
	}
}

// Real live-registry data: ai.dinglebear publishes both unraid-mcp and
// unraid-rmcp, one edit apart. Comparing full server names instead of
// publisher namespaces had them accusing each other of typosquatting.
func TestTyposquatSignal_SamePublisherSiblingsDoNotFlagEachOther(t *testing.T) {
	target := ServerRecord{Name: "ai.dinglebear/unraid-mcp", Packages: []Package{{Identifier: "unraid-mcp"}}}
	corpus := []ServerRecord{
		target,
		{Name: "ai.dinglebear/unraid-rmcp", Packages: []Package{{Identifier: "unraid-rmcp"}}},
	}
	sig := typosquatSignal(target, corpus)
	if sig.Impact < 0 {
		t.Errorf("a publisher's own sibling package must not be flagged as a typosquat: %+v", sig)
	}
}

func TestTyposquatSignal_DifferentPublisherStillFlags(t *testing.T) {
	target := ServerRecord{Name: "io.evil/pg", Packages: []Package{{Identifier: "mcp-server-postgress"}}}
	corpus := []ServerRecord{
		target,
		{Name: "io.github.real/pg", Packages: []Package{{Identifier: "mcp-server-postgres"}}},
	}
	sig := typosquatSignal(target, corpus)
	if !sig.Available || sig.Impact >= 0 {
		t.Fatalf("a different publisher's near-duplicate must still flag: %+v", sig)
	}
	if !strings.Contains(sig.Detail, "io.github.real") {
		t.Errorf("detail should name the other namespace so the claim is checkable, got %q", sig.Detail)
	}
}

func TestPublisherNamespace(t *testing.T) {
	for in, want := range map[string]string{
		"ai.dinglebear/unraid-mcp": "ai.dinglebear",
		"io.github.foo/bar":        "io.github.foo",
		"no-slash":                 "no-slash",
		"":                         "",
	} {
		if got := publisherNamespace(in); got != want {
			t.Errorf("publisherNamespace(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOSVEcosystem_CoversWhatTheRegistryActuallyPublishes(t *testing.T) {
	// Measured over a 6,000-server live sample: npm 1464, pypi 753, oci 101,
	// nuget 20, mcpb 17. nuget was silently unchecked despite OSV supporting
	// it, and "docker" was a key that matched nothing (the real value is oci).
	for _, supported := range []string{"npm", "pypi", "nuget"} {
		if _, ok := osvEcosystem[supported]; !ok {
			t.Errorf("registryType %q is published by the real registry and OSV supports it, but it is unmapped", supported)
		}
	}
	if _, ok := osvEcosystem["docker"]; ok {
		t.Error(`"docker" is not a registryType the registry emits — the real value is "oci"`)
	}
	for _, unsupported := range []string{"oci", "mcpb"} {
		if _, ok := osvEcosystem[unsupported]; ok {
			t.Errorf("OSV has no ecosystem for %q; mapping it would query a name OSV rejects", unsupported)
		}
	}
}

func TestTyposquatSignal_NoFlagWhenNothingClose(t *testing.T) {
	target := ServerRecord{Name: "a", Packages: []Package{{Identifier: "totally-unique-name-xyz"}}}
	corpus := []ServerRecord{
		target,
		{Name: "b", Packages: []Package{{Identifier: "completely-different-thing"}}},
	}
	sig := typosquatSignal(target, corpus)
	if !sig.Available || sig.Impact != 0 {
		t.Fatalf("expected no flag, got %+v", sig)
	}
}

func TestTyposquatSignal_SkipsEmptyIdentifiersOnBothSides(t *testing.T) {
	target := ServerRecord{Name: "a", Packages: []Package{{Identifier: ""}, {Identifier: "real-name"}}}
	corpus := []ServerRecord{
		target,
		{Name: "b", Packages: []Package{{Identifier: ""}}},
		{Name: "c", Packages: []Package{{Identifier: "totally-different-string"}}},
	}
	sig := typosquatSignal(target, corpus)
	if !sig.Available || sig.Impact != 0 {
		t.Fatalf("empty identifiers on either side must never be compared, got %+v", sig)
	}
}

func TestNearestIdentifier(t *testing.T) {
	corpus := []ServerRecord{
		{Packages: []Package{{Identifier: "chrome-devtools-mcp"}}},
		{Packages: []Package{{Identifier: "chrome-debugger-mcp"}}},
	}
	nearest, dist := NearestIdentifier("chrome-mcp", corpus)
	if dist < 0 {
		t.Fatalf("expected a distance to be found, got %d", dist)
	}
	if nearest != "chrome-devtools-mcp" && nearest != "chrome-debugger-mcp" {
		t.Errorf("nearest = %q, want one of the two chrome candidates", nearest)
	}
}

func TestNearestIdentifier_SkipsEmptyIdentifiersInCorpus(t *testing.T) {
	corpus := []ServerRecord{
		{Packages: []Package{{Identifier: ""}}},
		{Packages: []Package{{Identifier: "real-candidate"}}},
	}
	nearest, dist := NearestIdentifier("real-candidat", corpus)
	if nearest != "real-candidate" || dist != 1 {
		t.Errorf("got (%q, %d), want (\"real-candidate\", 1) — the empty identifier must never be returned as the nearest match", nearest, dist)
	}
}

func TestNearestIdentifier_EmptyCorpus(t *testing.T) {
	nearest, dist := NearestIdentifier("anything", nil)
	if nearest != "" || dist != -1 {
		t.Errorf("got (%q, %d), want (\"\", -1) for an empty corpus", nearest, dist)
	}
}

func TestKnownBadSignal_UnsupportedEcosystemIsUnavailable(t *testing.T) {
	sig := knownBadSignal(context.Background(), Package{RegistryType: "docker", Identifier: "x"})
	if sig.Available {
		t.Errorf("docker packages have no OSV ecosystem yet; expected Available=false, got %+v", sig)
	}
}

func TestKnownBadSignal_EmptyIdentifierIsUnavailable(t *testing.T) {
	sig := knownBadSignal(context.Background(), Package{RegistryType: "npm", Identifier: ""})
	if sig.Available {
		t.Errorf("an unresolved (empty) identifier has nothing to query; expected Available=false, got %+v", sig)
	}
}

func TestKnownBadSignal_NonOKStatusIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	orig := osvQueryURL
	osvQueryURL = srv.URL
	defer func() { osvQueryURL = orig }()

	sig := knownBadSignal(context.Background(), Package{RegistryType: "npm", Identifier: "x"})
	if sig.Available {
		t.Errorf("a 500 from OSV.dev must not be treated as evaluated evidence, got %+v", sig)
	}
}

func TestKnownBadSignal_MalformedResponseIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()
	orig := osvQueryURL
	osvQueryURL = srv.URL
	defer func() { osvQueryURL = orig }()

	sig := knownBadSignal(context.Background(), Package{RegistryType: "npm", Identifier: "x"})
	if sig.Available {
		t.Errorf("a malformed response body must not be treated as evaluated evidence, got %+v", sig)
	}
}

func TestKnownBadSignal_ParsesVulnResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(osvResponse{Vulns: []osvVuln{{ID: "GHSA-test-1234"}}})
	}))
	defer srv.Close()
	orig := osvQueryURL
	osvQueryURL = srv.URL
	defer func() { osvQueryURL = orig }()

	sig := knownBadSignal(context.Background(), Package{RegistryType: "npm", Identifier: "evil-pkg"})
	if !sig.Available || sig.Impact != -100 {
		t.Fatalf("expected a severe known-bad flag, got %+v", sig)
	}
}

// A real OSV.dev response's database_specific field is an OBJECT
// (malicious-packages-origins, severity, cwe_ids, ...), not the array an
// earlier version of osvVuln mistakenly declared it as — which made
// json.Decode fail on every real advisory carrying that field, silently
// turning the single highest-value signal in this whole scoring engine into
// a permanent no-op. This test uses the real shape (verbatim from a live
// query against postmark-mcp) precisely so that regression can't recur
// silently: TestKnownBadSignal_ParsesVulnResponse alone couldn't catch it
// because its synthetic fixture never included the field the real API does.
func TestKnownBadSignal_ToleratesRealOSVResponseShape(t *testing.T) {
	const realWorldShape = `{"vulns":[{"id":"MAL-2025-47604","summary":"Malicious code in postmark-mcp (npm)",
		"details":"turned malicious in v1.0.16","modified":"2025-09-26T04:14:45Z","published":"2025-09-26T04:14:45Z",
		"database_specific":{"malicious-packages-origins":[{"modified_time":"2025-09-26T04:14:45Z",
		"ranges":[{"events":[{"introduced":"1.0.16"}],"type":"SEMVER"}],"source":"google-open-source-security"}]},
		"references":[{"type":"ARTICLE","url":"https://example.com"}],"schema_version":"1.7.3"}]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(realWorldShape))
	}))
	defer srv.Close()
	orig := osvQueryURL
	osvQueryURL = srv.URL
	defer func() { osvQueryURL = orig }()

	sig := knownBadSignal(context.Background(), Package{RegistryType: "npm", Identifier: "postmark-mcp"})
	if !sig.Available || sig.Impact != -100 {
		t.Fatalf("real-shaped OSV response should still be caught as severe, got %+v", sig)
	}
}

func TestKnownBadSignal_NoVulnsIsNeutral(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(osvResponse{})
	}))
	defer srv.Close()
	orig := osvQueryURL
	osvQueryURL = srv.URL
	defer func() { osvQueryURL = orig }()

	sig := knownBadSignal(context.Background(), Package{RegistryType: "npm", Identifier: "fine-pkg"})
	if !sig.Available || sig.Impact != 0 {
		t.Fatalf("expected a neutral, available signal, got %+v", sig)
	}
}

func TestParseGitHubURL(t *testing.T) {
	tests := []struct {
		url                 string
		wantOwner, wantRepo string
		wantOK              bool
	}{
		{"https://github.com/foo/bar", "foo", "bar", true},
		{"https://github.com/foo/bar.git", "foo", "bar", true},
		{"https://github.com/foo/bar/", "foo", "bar", true},
		{"https://github.com/foo/bar/tree/main/subdir", "foo", "bar", true},
		{"https://gitlab.com/foo/bar", "", "", false},
		{"not a url", "", "", false},
	}
	for _, tt := range tests {
		owner, repo, ok := parseGitHubURL(tt.url)
		if ok != tt.wantOK || owner != tt.wantOwner || repo != tt.wantRepo {
			t.Errorf("parseGitHubURL(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.url, owner, repo, ok, tt.wantOwner, tt.wantRepo, tt.wantOK)
		}
	}
}

func TestMaintenanceSignal_NonGitHubIsUnavailable(t *testing.T) {
	sig := maintenanceSignal(context.Background(), "https://gitlab.com/foo/bar")
	if sig.Available {
		t.Errorf("expected Available=false for a non-GitHub repo, got %+v", sig)
	}
}

func TestMaintenanceSignal_ArchivedIsPenalized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(githubRepo{Archived: true})
	}))
	defer srv.Close()
	orig := githubAPIBase
	githubAPIBase = srv.URL
	defer func() { githubAPIBase = orig }()

	sig := maintenanceSignal(context.Background(), "https://github.com/foo/bar")
	if !sig.Available || sig.Impact >= 0 {
		t.Fatalf("expected a negative-impact archived flag, got %+v", sig)
	}
}

func TestMaintenanceSignal_RateLimitedIsUnavailableWithDetail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	orig := githubAPIBase
	githubAPIBase = srv.URL
	defer func() { githubAPIBase = orig }()

	sig := maintenanceSignal(context.Background(), "https://github.com/foo/bar")
	if sig.Available {
		t.Errorf("a rate-limited response must not be treated as evaluated evidence, got %+v", sig)
	}
	if sig.Detail == "" {
		t.Error("expected a detail explaining why this signal is unavailable")
	}
}

// An unevaluated category contributes the neutral baseline, so a repo
// confirmed to be actively maintained must score strictly above it —
// otherwise "checked and healthy" ties with "never checked".
func TestMaintenanceSignal_RecentPushIsPositive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(githubRepo{PushedAt: time.Now().Add(-24 * time.Hour)})
	}))
	defer srv.Close()
	orig := githubAPIBase
	githubAPIBase = srv.URL
	defer func() { githubAPIBase = orig }()

	sig := maintenanceSignal(context.Background(), "https://github.com/foo/bar")
	if !sig.Available || sig.Impact <= 0 {
		t.Fatalf("a recently-pushed, non-archived repo should score above the neutral baseline, got %+v", sig)
	}
}

// "Nothing to compare against" is not "compared and clean" — claiming the
// latter was enough on its own to keep a wholly-unknown server out of the
// insufficient-evidence state.
func TestTyposquatSignal_UnavailableWhenThereIsNothingToCompare(t *testing.T) {
	noCorpus := typosquatSignal(ServerRecord{Name: "a", Packages: []Package{{Identifier: "x"}}}, nil)
	if noCorpus.Available {
		t.Errorf("with no synced corpus the check did not run; got %+v", noCorpus)
	}
	noIdentifier := typosquatSignal(ServerRecord{Name: "a"}, []ServerRecord{
		{Name: "b", Packages: []Package{{Identifier: "y"}}},
	})
	if noIdentifier.Available {
		t.Errorf("with no identifier of our own there is nothing to compare; got %+v", noIdentifier)
	}
}

// The whole point of the third state: a server nothing could be checked
// about must say so, not render a number. This was unreachable in production
// before — every scored server got at least one always-on signal.
func TestComputeScore_TrulyUnknownServerSaysInsufficientEvidence(t *testing.T) {
	score := ComputeScore(context.Background(), ServerRecord{Name: "io.example/unknown"}, nil)
	if score.Confidence != ConfidenceInsufficient {
		t.Errorf("Confidence = %q, want insufficient_evidence", score.Confidence)
	}
	if got := FormatScore(score); got != "insufficient evidence" {
		t.Errorf("FormatScore = %q, want %q — a number here would be a claim we cannot support", got, "insufficient evidence")
	}
}

func TestMaintenanceSignal_StalePushIsPenalized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(githubRepo{PushedAt: time.Now().Add(-365 * 24 * time.Hour)})
	}))
	defer srv.Close()
	orig := githubAPIBase
	githubAPIBase = srv.URL
	defer func() { githubAPIBase = orig }()

	sig := maintenanceSignal(context.Background(), "https://github.com/foo/bar")
	if !sig.Available || sig.Impact >= 0 {
		t.Fatalf("a year-stale repo should be penalized, got %+v", sig)
	}
}

func TestMaintenanceSignal_NonOKStatusIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	orig := githubAPIBase
	githubAPIBase = srv.URL
	defer func() { githubAPIBase = orig }()

	sig := maintenanceSignal(context.Background(), "https://github.com/foo/bar")
	if sig.Available {
		t.Errorf("a 404 (e.g. deleted repo) must not be treated as evaluated evidence, got %+v", sig)
	}
}

func TestDeclaredAuthSignal_StdioIsNotApplicable(t *testing.T) {
	sig := declaredAuthSignal(ServerRecord{})
	if sig.Available {
		t.Errorf("a server with no remotes should report Available=false, got %+v", sig)
	}
}

func TestDeclaredAuthSignal_RemoteWithSecretHeaderIsPositive(t *testing.T) {
	sig := declaredAuthSignal(ServerRecord{Remotes: []Remote{
		{Type: "http", URL: "https://x", Headers: []RemoteHeader{{Name: "Authorization", IsSecret: true}}},
	}})
	if !sig.Available || sig.Impact <= 0 {
		t.Fatalf("expected a positive declared-auth signal, got %+v", sig)
	}
}

func TestDeclaredAuthSignal_RemoteWithNoAuthIsNegative(t *testing.T) {
	sig := declaredAuthSignal(ServerRecord{Remotes: []Remote{{Type: "http", URL: "https://x"}}})
	if !sig.Available || sig.Impact >= 0 {
		t.Fatalf("expected a negative no-auth signal, got %+v", sig)
	}
}

func TestAggregate_NoAvailableSignalsIsInsufficientEvidence(t *testing.T) {
	cs := aggregate([]Signal{{Available: false}, {Available: false}})
	if cs.Confidence != ConfidenceInsufficient {
		t.Errorf("Confidence = %q, want insufficient_evidence", cs.Confidence)
	}
}

func TestAggregate_ClampsToZeroAndHundred(t *testing.T) {
	low := aggregate([]Signal{{Available: true, Impact: -1000}})
	if low.Value != 0 {
		t.Errorf("Value = %v, want clamped to 0", low.Value)
	}
	high := aggregate([]Signal{{Available: true, Impact: 1000}})
	if high.Value != 100 {
		t.Errorf("Value = %v, want clamped to 100", high.Value)
	}
}

func TestComputeScore_InsufficientEvidenceOverallWhenNothingResolves(t *testing.T) {
	// A server with an unsupported-ecosystem package, a non-GitHub repo, and
	// no remotes has every signal come back unavailable except the baseline
	// registry-presence signal (Community & Governance) — overall confidence
	// should reflect that most categories are insufficient, not average them
	// in as if they were neutral zeros.
	srv := ServerRecord{
		Name:       "x",
		Packages:   []Package{{RegistryType: "docker", Identifier: "img"}},
		Repository: Repository{URL: "https://gitlab.com/foo/bar"},
	}
	score := ComputeScore(context.Background(), srv, []ServerRecord{srv})
	if score.SecurityImplementation.Confidence != ConfidenceInsufficient {
		// typosquatSignal is always available (pure algorithm), so Security
		// Implementation has partial evidence even though knownBadSignal
		// isn't — that's correct, not a bug: assert on the categories that
		// truly have nothing.
		t.Logf("security implementation: %+v (expected partial evidence from typosquatSignal)", score.SecurityImplementation)
	}
	if score.RepositoryHealth.Confidence != ConfidenceInsufficient {
		t.Errorf("RepositoryHealth.Confidence = %q, want insufficient_evidence (non-GitHub repo)", score.RepositoryHealth.Confidence)
	}
	if score.OperationalSecurity.Confidence != ConfidenceInsufficient {
		t.Errorf("OperationalSecurity.Confidence = %q, want insufficient_evidence (no remotes)", score.OperationalSecurity.Confidence)
	}
	// The registry-presence signal is context, not evidence: it is true of
	// every scored server, so counting it manufactured confidence about a
	// server nothing had actually been checked about.
	if score.CommunityGovernance.Confidence != ConfidenceInsufficient {
		t.Errorf("CommunityGovernance.Confidence = %q, want insufficient_evidence — a constant every server gets is not evidence", score.CommunityGovernance.Confidence)
	}
	if len(score.CommunityGovernance.Signals) == 0 {
		t.Error("the presence signal should still be listed for the reader, just not counted")
	}
}

// The bug this guards: renormalising over available categories only meant an
// identical server scored HIGHER when GitHub was rate-limited (73) than when
// GitHub answered and confirmed a recent push (72). Absence of evidence must
// never beat presence of good evidence.
func TestComputeScore_MissingEvidenceNeverBeatsGoodEvidence(t *testing.T) {
	srv := ServerRecord{
		Name:       "io.github.x/y",
		Packages:   []Package{{RegistryType: "npm", Identifier: "pkg"}},
		Repository: Repository{URL: "https://github.com/x/y"},
	}
	osv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	defer osv.Close()
	origOSV := osvQueryURL
	osvQueryURL = osv.URL
	defer func() { osvQueryURL = origOSV }()

	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(githubRepo{PushedAt: time.Now().Add(-24 * time.Hour)})
	}))
	defer healthy.Close()
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer limited.Close()

	orig := githubAPIBase
	defer func() { githubAPIBase = orig }()

	githubAPIBase = healthy.URL
	checked := ComputeScore(context.Background(), srv, nil)
	githubAPIBase = limited.URL
	unchecked := ComputeScore(context.Background(), srv, nil)

	if unchecked.Overall > checked.Overall {
		t.Errorf("rate-limited score %.1f beats verified-healthy score %.1f — failing to check must not raise the score",
			unchecked.Overall, checked.Overall)
	}
	if unchecked.Confidence == checked.Confidence {
		t.Errorf("confidence should differ: verified=%q rate-limited=%q", checked.Confidence, unchecked.Confidence)
	}
}

func TestOverallConfidence_AllFourBands(t *testing.T) {
	tests := []struct {
		substantive int
		want        Confidence
	}{
		{0, ConfidenceInsufficient},
		{-1, ConfidenceInsufficient},
		{1, ConfidenceLow},
		{2, ConfidenceModerate},
		{3, ConfidenceHigh},
		{4, ConfidenceHigh},
	}
	for _, tt := range tests {
		if got := overallConfidence(tt.substantive); got != tt.want {
			t.Errorf("overallConfidence(%d) = %q, want %q", tt.substantive, got, tt.want)
		}
	}
}

func TestFormatScore_InsufficientEvidence(t *testing.T) {
	s := Score{Confidence: ConfidenceInsufficient}
	if got := FormatScore(s); got != "insufficient evidence" {
		t.Errorf("FormatScore = %q", got)
	}
}

func TestFormatScore_Rounds(t *testing.T) {
	s := Score{Overall: 72.6, Confidence: ConfidenceHigh}
	if got := FormatScore(s); got != "73/100" {
		t.Errorf("FormatScore = %q, want 73/100", got)
	}
}
