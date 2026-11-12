// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type attestJob struct {
	Permissions map[string]string `yaml:"permissions"`
	Uses        string            `yaml:"uses"`
	Steps       []struct {
		Name string         `yaml:"name"`
		Uses string         `yaml:"uses"`
		With map[string]any `yaml:"with"`
	} `yaml:"steps"`
}

func attestJobs(t *testing.T, file string) map[string]attestJob {
	t.Helper()
	var wf struct {
		Jobs map[string]attestJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(workflowFile(t, file)), &wf); err != nil {
		t.Fatal(err)
	}
	return wf.Jobs
}

// Provenance is signed with the job's OIDC token and stored through the
// attestations API; without either permission the step fails at release time.
func TestPublish_AttestsWhatGoReleaserBuilt(t *testing.T) {
	bin := attestJobs(t, "publish.yml")["binaries"]
	for _, p := range []string{"id-token", "attestations"} {
		if bin.Permissions[p] != "write" {
			t.Errorf("publish.yml binaries has %s: %q, want write", p, bin.Permissions[p])
		}
	}
	built, attested := -1, -1
	for i, s := range bin.Steps {
		switch {
		case strings.HasPrefix(s.Uses, "goreleaser/goreleaser-action@"):
			built = i
		case strings.HasPrefix(s.Uses, "actions/attest-build-provenance@"):
			attested = i
			if v, _ := s.With["subject-checksums"].(string); v != "dist/checksums.txt" {
				t.Errorf("the attestation covers %v, want every file in dist/checksums.txt", s.With)
			}
		}
	}
	if attested < 0 || attested < built {
		t.Error("publish.yml does not attest the GoReleaser output after building it")
	}
}

// A reusable workflow gets at most its caller's permissions.
func TestPublishCallers_GrantAttestations(t *testing.T) {
	for _, file := range []string{"release-please.yml", "release.yml"} {
		for id, j := range attestJobs(t, file) {
			if strings.HasSuffix(j.Uses, "/publish.yml") && j.Permissions["attestations"] != "write" {
				t.Errorf("%s job %s calls publish.yml without attestations: write", file, id)
			}
		}
	}
}

// A hash only says the file matches checksums.txt, which sits on the same
// release. The signer workflow says this repo's publish job built it.
func TestReleaseSmoke_VerifiesProvenance(t *testing.T) {
	src := repoFile(t, "scripts", "release-smoke.sh")
	if !strings.Contains(src, "gh attestation verify") ||
		!strings.Contains(src, `--signer-workflow "$REPO/.github/workflows/publish.yml"`) {
		t.Error("release-smoke.sh does not verify the archive's provenance against publish.yml")
	}
}
