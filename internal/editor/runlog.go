// SPDX-License-Identifier: MIT

package editor

import (
	"fmt"
	"sync/atomic"

	"github.com/ankit373/hydra/internal/runid"
	"github.com/ankit373/hydra/internal/runlog"
)

// editSeq numbers snapshots within a process so two edits in one run cannot
// overwrite each other's content. The run id keeps separate runs apart; this
// keeps separate edits apart inside one.
var editSeq atomic.Uint64

// logEdit records one applied edit: the file and line counts on the event, the
// before/after content in the run's side-file store.
//
// The content is never inlined. The run log's atomicity guarantee is per
// write() call, so an entry carrying a whole file would break the property that
// makes concurrent appends safe — hence Ref, pointing at content held beside
// the log.
//
// Best-effort throughout: an edit that succeeded must not be reported as failed
// because its observability record could not be written.
func logEdit(req Request, before, after string, added, removed int) {
	runID := runid.ResolveRun(req.RunID)
	taskID := runid.ResolveTask(req.TaskID)
	ref := fmt.Sprintf("%06d", editSeq.Add(1))

	detail := fmt.Sprintf("+%d/-%d", added, removed)
	if err := runlog.SaveEdit(runID, ref, []byte(before), []byte(after)); err != nil {
		// The event still goes out. Losing it would hide that the file changed
		// at all, which is worse than losing the diff — the reader can render
		// "diff unavailable" from an unresolvable ref.
		ref = ""
		detail += " · snapshot unavailable"
	}

	_ = runlog.New(runID).Append(runlog.Event{
		Kind:   runlog.KindEdit,
		TaskID: taskID,
		File:   req.File,
		Ref:    ref,
		Detail: detail,
	})
}
