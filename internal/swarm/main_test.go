// SPDX-License-Identifier: MIT

package swarm

import (
	"os"
	"testing"
)

// TestMain points HOME at a temp directory for the whole package.
//
// Swarm reaches trust.DefaultCoAgreementPath() and trust.DefaultPath(), which
// resolve config.Dir() at call time, so a test that forgets to sandbox appends
// to the developer's real ~/.hydra. That is not hypothetical: 701 rows of
// fixture, including a fabricated "acme" model family, reached a real store
// and drove a live "these heads vote as one" finding in `hyctl security`.
//
// Individual tests still call withTempConfig when they need a config file;
// this is the floor beneath them, so forgetting cannot reach real user data.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "hydra-swarm-test-home")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	os.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
