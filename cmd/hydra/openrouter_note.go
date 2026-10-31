// SPDX-License-Identifier: MIT

package main

import (
	"fmt"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/pricing"
)

// openRouterCounts is the allowlist size and the catalogue size, the second
// read only when the first is non-zero so a machine with no allowlist never
// touches the pricing cache to render a note it will not print.
func openRouterCounts() (enabled, catalogue int) {
	enabled = len(config.OpenRouterModels())
	if enabled == 0 {
		return 0, 0
	}
	return enabled, len(pricing.Load().Models())
}

// openRouterNote says how much of OpenRouter's catalogue the allowlist admits.
// #717: "12 of 327 models enabled" is honest, a list quietly cut down to a
// dozen is the #714 defect. Empty when nothing is configured, since the single
// key-derived head is then what it has always been and there is nothing to
// explain.
func openRouterNote(enabled, catalogue int, configPath string) string {
	switch {
	case enabled == 0:
		return ""
	case catalogue == 0:
		// The fetch never landed, so the denominator is unknown. Saying "of 0"
		// would read as a catalogue with nothing in it.
		return fmt.Sprintf("%d OpenRouter models enabled from %s; "+
			"the catalogue has not been fetched, so none could be checked "+
			"(`hyctl pricing refresh`).", enabled, configPath)
	default:
		return fmt.Sprintf("%d of %d OpenRouter models enabled; "+
			"name more under [openrouter] in %s.", enabled, catalogue, configPath)
	}
}
