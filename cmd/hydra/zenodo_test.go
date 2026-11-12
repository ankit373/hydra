// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Zenodo's GitHub integration archived every release, edge builds included,
// and each one is a DOI that can never be deleted. The publish job replaces it
// and must stay limited to stable tags.

type zenodoJob struct {
	Needs any    `yaml:"needs"`
	If    string `yaml:"if"`
	Steps []struct {
		Name string `yaml:"name"`
		If   string `yaml:"if"`
		Run  string `yaml:"run"`
		Uses string `yaml:"uses"`
	} `yaml:"steps"`
}

func publishJobs(t *testing.T) map[string]zenodoJob {
	t.Helper()
	var wf struct {
		Jobs map[string]zenodoJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(workflowFile(t, "publish.yml")), &wf); err != nil {
		t.Fatal(err)
	}
	return wf.Jobs
}

func TestZenodo_ArchivesStableReleasesOnly(t *testing.T) {
	jobs := publishJobs(t)
	z, ok := jobs["zenodo"]
	if !ok {
		t.Fatal("publish.yml has no zenodo job; this test no longer checks what it thinks")
	}
	if z.If != "${{ !contains(inputs.tag, '-') }}" {
		t.Errorf("zenodo job runs if %q; it must skip every tag with a prerelease suffix", z.If)
	}
	if !strings.Contains(repoFile(t, "scripts", "zenodo-publish.sh"), `'^v[0-9]+\.[0-9]+\.[0-9]+$'`) {
		t.Error("zenodo-publish.sh does not refuse a prerelease tag itself, so a manual run could archive one")
	}
}

// No token means a clean skip, not a red release; and a Zenodo failure must
// not stop any channel, so nothing may depend on the job.
func TestZenodo_SkipsWithoutATokenAndBlocksNothing(t *testing.T) {
	jobs := publishJobs(t)
	for _, s := range jobs["zenodo"].Steps {
		if s.Name == "Missing token" {
			if strings.Contains(s.Run, "exit 1") {
				t.Error("a missing ZENODO_TOKEN fails the release; it should only leave a notice")
			}
			continue
		}
		if s.If != "${{ env.ZENODO_TOKEN != '' }}" {
			t.Errorf("zenodo step %q runs without a token: if %q", s.Name+s.Uses, s.If)
		}
	}
	for id, j := range jobs {
		for _, n := range conditionStrings(j.Needs) {
			if n == "zenodo" {
				t.Errorf("job %s waits on zenodo, so a Zenodo outage would hold up that channel", id)
			}
		}
	}
}

// .zenodo.json and CITATION.cff describe the same thing to two readers.
func TestCitationMetadata_AgreesWithZenodo(t *testing.T) {
	var z struct {
		UploadType  string `json:"upload_type"`
		Title       string `json:"title"`
		Description string `json:"description"`
		License     string `json:"license"`
		Creators    []struct {
			Name string `json:"name"`
		} `json:"creators"`
	}
	if err := json.Unmarshal([]byte(repoFile(t, ".zenodo.json")), &z); err != nil {
		t.Fatalf(".zenodo.json: %v", err)
	}
	var c struct {
		Version string `yaml:"cff-version"`
		Title   string `yaml:"title"`
		License string `yaml:"license"`
		Authors []struct {
			Family string `yaml:"family-names"`
			Given  string `yaml:"given-names"`
		} `yaml:"authors"`
	}
	if err := yaml.Unmarshal([]byte(repoFile(t, "CITATION.cff")), &c); err != nil {
		t.Fatalf("CITATION.cff: %v", err)
	}

	if z.UploadType != "software" || z.Description == "" || len(z.Creators) == 0 {
		t.Fatalf(".zenodo.json is missing fields Zenodo requires: %+v", z)
	}
	if c.Version != "1.2.0" {
		t.Errorf("CITATION.cff cff-version = %q, want 1.2.0", c.Version)
	}
	if z.Title != c.Title {
		t.Errorf("titles differ: .zenodo.json %q, CITATION.cff %q", z.Title, c.Title)
	}
	if !strings.EqualFold(z.License, c.License) || !strings.Contains(repoFile(t, "LICENSE"), "MIT License") {
		t.Errorf("licenses differ: .zenodo.json %q, CITATION.cff %q, LICENSE says MIT", z.License, c.License)
	}
	if len(c.Authors) == 0 || z.Creators[0].Name != c.Authors[0].Family+", "+c.Authors[0].Given {
		t.Errorf("first author differs: .zenodo.json %q, CITATION.cff %+v", z.Creators[0].Name, c.Authors)
	}
}
