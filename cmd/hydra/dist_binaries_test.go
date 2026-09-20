// SPDX-License-Identifier: MIT

package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A command that no release builds reaches nobody. `hyverify` shipped for a
// whole cycle with llms.txt calling it "a second binary" while .goreleaser.yaml
// built only ./cmd/hydra, so the one tool aimed at repos that have *not*
// adopted Hydra could only be had by cloning and running `go build`.
//
// Nothing failed, because nothing compared the two lists.

// mainPackages is every cmd/* directory that actually builds a binary.
func mainPackages(t *testing.T) []string {
	t.Helper()
	root := filepath.Join("..", "..", "cmd")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading cmd/: %v", err)
	}

	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		pkgs, err := parser.ParseDir(token.NewFileSet(), dir, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", dir, err)
		}
		if _, ok := pkgs["main"]; ok {
			out = append(out, "./cmd/"+e.Name())
		}
	}
	if len(out) == 0 {
		t.Fatal("no main packages under cmd/, this guard has stopped guarding")
	}
	return out
}

// Every binary the repo produces must have a release build, or it ships nowhere
// while the docs go on describing it.
func TestDist_EveryCommandHasAReleaseBuild(t *testing.T) {
	var doc struct {
		Builds []struct {
			ID     string `yaml:"id"`
			Main   string `yaml:"main"`
			Binary string `yaml:"binary"`
		} `yaml:"builds"`
	}
	if err := yaml.Unmarshal([]byte(repoFile(t, ".goreleaser.yaml")), &doc); err != nil {
		t.Fatalf("parsing .goreleaser.yaml: %v", err)
	}

	built := map[string]string{} // main path -> binary name
	for _, b := range doc.Builds {
		built[b.Main] = b.Binary
	}

	// Deliberately not shipped, with the reason. A new command is a decision
	// about distribution, so it fails here until someone makes that decision
	// rather than defaulting to invisible.
	notShipped := map[string]string{
		"./cmd/specstest": "a `go run` diagnostic for local-model sizing, " +
			"pointed at from an error message and advertised as a binary nowhere",
	}

	for _, pkg := range mainPackages(t) {
		if _, ok := built[pkg]; ok {
			continue
		}
		if _, ok := notShipped[pkg]; ok {
			continue
		}
		t.Errorf("%s builds a binary but .goreleaser.yaml has no build for it, "+
			"so no release archive carries it and the only way to get it is to "+
			"clone the repo and run `go build`.\n"+
			"  Add a build, or record it in notShipped here with the reason.", pkg)
	}

	for pkg := range notShipped {
		if _, ok := built[pkg]; ok {
			t.Errorf("%s is listed as not shipped but .goreleaser.yaml builds it; "+
				"the exception is now a lie", pkg)
		}
	}
}

// The archive must carry every built binary. goreleaser includes all builds
// when an archive names no ids, so an explicit list that forgets one is the
// way this silently regresses.
func TestDist_ArchiveCarriesEveryBuild(t *testing.T) {
	var doc struct {
		Builds []struct {
			ID string `yaml:"id"`
		} `yaml:"builds"`
		Archives []struct {
			ID  string   `yaml:"id"`
			IDs []string `yaml:"ids"`
		} `yaml:"archives"`
	}
	if err := yaml.Unmarshal([]byte(repoFile(t, ".goreleaser.yaml")), &doc); err != nil {
		t.Fatalf("parsing .goreleaser.yaml: %v", err)
	}
	if len(doc.Archives) == 0 {
		t.Fatal("no archives in .goreleaser.yaml, this guard has stopped guarding")
	}

	for _, a := range doc.Archives {
		if len(a.IDs) == 0 {
			continue // no ids means every build, which is what we want
		}
		named := map[string]bool{}
		for _, id := range a.IDs {
			named[id] = true
		}
		for _, b := range doc.Builds {
			if !named[b.ID] {
				t.Errorf("archive %q names ids %v, which leaves build %q out of the "+
					"release archive", a.ID, a.IDs, b.ID)
			}
		}
	}
}

// The Homebrew formula installs binaries by name, so a new build that the
// script never names is on the releases page and not on anyone's PATH.
func TestDist_TapFormulaInstallsEveryBinary(t *testing.T) {
	var doc struct {
		Builds []struct {
			Binary string `yaml:"binary"`
		} `yaml:"builds"`
	}
	if err := yaml.Unmarshal([]byte(repoFile(t, ".goreleaser.yaml")), &doc); err != nil {
		t.Fatalf("parsing .goreleaser.yaml: %v", err)
	}

	script := repoFile(t, "scripts", "update-tap-formula.sh")
	for _, b := range doc.Builds {
		if b.Binary == "" {
			continue
		}
		if !strings.Contains(script, `bin.install "`+b.Binary+`"`) {
			t.Errorf("goreleaser builds %q but scripts/update-tap-formula.sh never "+
				"installs it, so `brew install hyctl` does not put it on PATH", b.Binary)
		}
	}
}
