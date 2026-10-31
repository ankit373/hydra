// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"testing"
)

// `hyctl --version` answered "unknown flag: --version" while a `version`
// subcommand existed. It is the near-universal convention, so it is what people
// type first, and an error there reads like a broken install.
//
// Executing the real root command rather than inspecting fields: the point is
// what a user typing the flag actually gets.
func TestRootCommand_VersionFlagPrintsTheSameTextAsTheSubcommand(t *testing.T) {
	run := func(args ...string) string {
		var out bytes.Buffer
		root := rootCmd()
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatalf("hyctl %v: %v", args, err)
		}
		return out.String()
	}

	flag := run("--version")
	if flag == "" {
		t.Fatal("--version printed nothing")
	}
	if flag != versionText() {
		t.Errorf("--version does not match versionText():\n got %q\nwant %q", flag, versionText())
	}
}
