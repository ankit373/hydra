// SPDX-License-Identifier: MIT

package api

import (
	"github.com/ankit373/hydra/internal/probe"
	"github.com/ankit373/hydra/internal/security"
)

// GetSecurity assembles the security posture report. It mirrors the CLI's
// exact call shape (cmd/hydra/main.go's cmdSecurity) and returns
// *security.Report directly rather than a parallel DTO: Report is already
// the single well-formed aggregate for this domain, unlike Dashboard which
// merges three unrelated packages with no existing common shape.
func (a *API) GetSecurity() (*security.Report, error) {
	return security.Build(probe.Run(a.ctx).Heads)
}
