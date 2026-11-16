// SPDX-License-Identifier: MIT

package security

import (
	"fmt"

	"github.com/ankit373/hydra/internal/sandbox"
)

// OfflineCheck reports whether model-written code can be run with the network
// off here. isolation is sandbox.NetworkIsolation(), probed by the caller.
func OfflineCheck(isolation error) Check {
	c := Check{Name: "Offline runner"}
	if isolation != nil {
		c.Status = "not available"
		c.Detail = fmt.Sprintf("%v. Generated tests are not run here "+
			"and read as not checked, never a pass", isolation)
		return c
	}
	c.Status = "network off"
	c.Detail = "generated tests (--check gen:tests) run in " + sandbox.NetworkMethod + ", with no credentials " +
		"and bounded CPU and time. They can still read and write your files"
	return c
}
