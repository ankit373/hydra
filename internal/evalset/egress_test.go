// SPDX-License-Identifier: MIT

package evalset

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// net is the chokepoint every socket reaches through, so banning it covers
// net/http, net/smtp and crypto/tls alike; os/exec is the way around it.
// net/url parses and cannot send, so it is deliberately not here.
func egressVia(deps []string) string {
	for _, d := range deps {
		if d == "net" || d == "os/exec" {
			return d
		}
	}
	return ""
}

// The corpus is the user's own code and prompts, kept verbatim and marked
// rather than redacted, so "it never leaves this machine" is load-bearing.
// Until now the rule was a comment naming an export path that never existed.
func TestNothingReachableFromTheCorpusCanEgress(t *testing.T) {
	deps := packageDeps(t, "github.com/ankit373/hydra/internal/evalset")

	// A walk that returned nothing would pass by asserting nothing, which is
	// exactly how the shellpath guard went vacuous (#816). encoding/json is
	// reachable because Add marshals through it: absent, the walk is broken.
	if !slices.Contains(deps, "encoding/json") {
		t.Fatalf("dependency walk returned %d packages and no encoding/json, so it proves nothing", len(deps))
	}

	if got := egressVia(deps); got != "" {
		t.Errorf("%s is reachable from the corpus, which holds candidates verbatim "+
			"and marks PII rather than dropping it. Nothing here may leave the machine.", got)
	}
}

// The mirror: if the match were broken the test above would pass whatever the
// corpus imported, and report a promise it had not checked.
func TestEgressCheckCatchesWhatItClaimsTo(t *testing.T) {
	if got := egressVia([]string{"encoding/json", "net"}); got != "net" {
		t.Errorf("net was not caught (got %q), so the guard above proves nothing", got)
	}
	if got := egressVia([]string{"encoding/json", "os/exec"}); got != "os/exec" {
		t.Errorf("os/exec was not caught (got %q)", got)
	}
	if got := egressVia([]string{"encoding/json", "net/url", "path/filepath"}); got != "" {
		t.Errorf("a clean import set was reported as egress via %q", got)
	}
}

func packageDeps(t *testing.T, pkg string) []string {
	t.Helper()
	// go list -deps reports the package's own imports, never its test
	// imports, so this file's os/exec is not in what it measures.
	gocmd, err := exec.LookPath("go")
	if err != nil {
		gocmd = filepath.Join(runtime.GOROOT(), "bin", "go")
	}
	out, err := exec.Command(gocmd, "list", "-deps", pkg).Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", pkg, err)
	}
	return strings.Fields(string(out))
}
