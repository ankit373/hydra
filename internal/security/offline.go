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
		c.Detail = fmt.Sprintf("%v. Model-written code that must run offline is not run here, "+
			"and reads as not checked, never a pass", isolation)
		return c
	}
	c.Status = "network off"
	c.Detail = "model-written code that must run offline gets " + sandbox.NetworkMethod + ", with no credentials " +
		"and bounded CPU, memory and time. It can still read and write your files"
	return c
}
