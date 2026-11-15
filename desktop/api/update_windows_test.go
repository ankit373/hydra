// SPDX-License-Identifier: MIT

package api

import "testing"

// Windows has no in-app upgrade; it must say so rather than half-run one.
func TestTriggerUpgrade_RefusesOnWindows(t *testing.T) {
	withAppExecutable(t, `C:\Hydra\Hydra.exe`)
	if r := New().TriggerUpgrade(); r.OK || r.Output == "" {
		t.Errorf("TriggerUpgrade = %+v, want a refusal with a reason", r)
	}
}
