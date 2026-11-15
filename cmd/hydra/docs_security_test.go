// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The reporting channel must be one the project controls. SECURITY.md once sent
// reports to a stranger's domain and cited a script that no longer existed.

const advisoryURL = "https://github.com/ankit373/hydra/security/advisories/new"

func TestDocs_SecurityReportsGoToPrivateAdvisories(t *testing.T) {
	for _, f := range []string{"SECURITY.md", ".github/ISSUE_TEMPLATE/config.yml"} {
		if !strings.Contains(repoFile(t, f), advisoryURL) {
			t.Errorf("%s does not link %s", f, advisoryURL)
		}
	}
	if !strings.Contains(repoFile(t, "docs", ".well-known", "security.txt"), "Contact: "+advisoryURL) {
		t.Errorf("docs/.well-known/security.txt Contact is not %s", advisoryURL)
	}
	if m := regexp.MustCompile(`[\w.+-]+@[\w-]+\.[\w.-]+`).FindString(repoFile(t, "SECURITY.md")); m != "" {
		t.Errorf("SECURITY.md lists email %q; reports go through the private advisory form only", m)
	}
}

// A path SECURITY.md names as a control must exist, or the policy is fiction.
func TestDocs_SecurityPolicyPathsExist(t *testing.T) {
	// Repo-relative only: home paths (~, $HYDRA_HOME) and URLs are skipped.
	rx := regexp.MustCompile("`([A-Za-z][\\w.-]*/[^`\\s:<]+)`")
	matches := rx.FindAllStringSubmatch(repoFile(t, "SECURITY.md"), -1)
	if len(matches) == 0 {
		t.Fatal("SECURITY.md names no repo paths; this test has stopped testing anything")
	}
	for _, m := range matches {
		if _, err := os.Stat(filepath.Join("..", "..", m[1])); err != nil {
			t.Errorf("SECURITY.md names %s, which does not exist", m[1])
		}
	}
}
