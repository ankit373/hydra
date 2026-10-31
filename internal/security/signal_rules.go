// SPDX-License-Identifier: MIT

package security

import (
	"fmt"
	"strings"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/signals"
)

// signalRulesControl reports registry/signals.yaml: whether any rule is
// declared, and whether any of them can never fire.
//
// Read off the loaded engine rather than the file, so a rule that parsed but
// sits behind an unconditional one is reported as dead. That is the one case
// loading cannot call an error: the file is valid, the rule is just
// unreachable, which is the same shape as policy.yaml's dead conditions (#854).
func signalRulesControl() Control {
	c := Control{
		Name:     "Routing signal rules",
		Declared: true,
		Verified: true,
	}

	eng, err := signals.Load(config.ScriptHome(), nil)
	if err != nil {
		// A rules file that will not load stops every dispatch, so this is a
		// finding rather than an absence.
		c.Detail = fmt.Sprintf("registry/%s does not load: %v. Every dispatch fails until it does",
			signals.FileName, err)
		return c
	}

	rules := eng.Rules()
	if len(rules) == 0 {
		c.Detail = fmt.Sprintf("registry/%s declares no rules, so routing is unchanged by it. "+
			"%d signals are available to rules that want them",
			signals.FileName, len(eng.Signals()))
		return c
	}

	c.Wired = true
	c.Detail = fmt.Sprintf("%d rule(s) declared over %d available signals, evaluated once per "+
		"dispatch in priority order, first match wins",
		len(rules), len(eng.Signals()))

	if inert := eng.Inert(); len(inert) > 0 {
		c.Limited = true
		c.Detail += fmt.Sprintf(". %d rule(s) can never fire: %s",
			len(inert), strings.Join(inert, "; "))
	}
	return c
}
