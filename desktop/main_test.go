// SPDX-License-Identifier: MIT

package main

import (
	"testing"

	"github.com/ankit373/hydra/internal/provider"
)

// The desktop binary needs the same self-registering provider imports as
// cmd/hydra/main.go, without them provider.All() is empty and every chat
// dispatch reports zero heads, no matter what is actually installed (#495).
// desktop/api's own tests cannot catch this: they compile a separate binary
// that never imports this package.
func TestProviderPluginsAreRegistered(t *testing.T) {
	if len(provider.All()) == 0 {
		t.Fatal("provider.All() is empty, the desktop binary is missing the blank " +
			"imports of internal/provider/{cli,agy,env,port}, so it can never discover a head")
	}
}
