// SPDX-License-Identifier: MIT

package editor

import (
	"github.com/ankit373/hydra/internal/runid"
	"github.com/ankit373/hydra/internal/runlog"
)

// logEdit records one applied edit via runlog.LogEdit, resolving the run/task
// identity the same way every other event on this edit does.
func logEdit(req Request, before, after string, added, removed int) {
	runID := runid.ResolveRun(req.RunID)
	taskID := runid.ResolveTask(req.TaskID)
	runlog.LogEdit(runID, taskID, req.File, []byte(before), []byte(after), added, removed)
}
