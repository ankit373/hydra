import { useEffect, useState } from 'react'
import { ApproveEdit, GetDiff, RejectEdit } from '../bindings'
import type { Diff, DiffLine, Edit, ReviewOutcome } from '../types'

export function Code({
  runID,
  edits,
  initialFile,
}: {
  runID: string
  edits: Edit[]
  /** Jump straight to this file — set when a graph node was clicked (#518)
   *  rather than the tab reached on its own, which just wants index 0. */
  initialFile?: string
}) {
  const [selected, setSelected] = useState(0)
  const [diff, setDiff] = useState<Diff | null>(null)
  // Keyed by file, not index: the edit list can reorder under a poll.
  // Session-only, deliberately — nothing persists a per-file review
  // state, and inventing one here would claim more than Hydra records.
  const [acted, setActed] = useState<Record<string, ReviewOutcome>>({})
  const [busy, setBusy] = useState(false)

  // Re-selects on every new value, not just at mount — Session stays mounted
  // across clicking different artifact nodes in the same run, so this has to
  // react to initialFile changing, not just seed the initial state.
  useEffect(() => {
    if (!initialFile) return
    const i = edits.findIndex((e) => e.file === initialFile)
    if (i >= 0) setSelected(i)
  }, [initialFile, edits])

  const current = edits[selected]

  async function act(file: string, kind: 'approve' | 'reject') {
    if (busy) return
    setBusy(true)
    try {
      const out = kind === 'approve' ? await ApproveEdit(file) : await RejectEdit(file)
      setActed((a) => ({ ...a, [file]: out }))
    } catch (e) {
      const message = e instanceof Error ? e.message : String(e)
      setActed((a) => ({ ...a, [file]: { file, error: message } }))
    } finally {
      setBusy(false)
    }
  }

  useEffect(() => {
    if (!current) {
      setDiff(null)
      return
    }
    let stale = false
    GetDiff(runID, current.ref ?? '', current.file)
      .then((d) => {
        if (!stale) setDiff(d)
      })
      .catch(() => {
        if (!stale) setDiff(null)
      })
    return () => {
      stale = true
    }
  }, [runID, current])

  if (edits.length === 0) {
    return (
      <div className="empty">
        <p className="empty__title">This run changed no files</p>
        <p>
          File edits appear here. Try <code>hyctl edit --file … --prompt "…"</code>.
        </p>
      </div>
    )
  }

  return (
    <div className="code">
      <div className="code__side">
        {/* The per-file counts were there; the total was not, so "how big is
            this change" meant adding the column up by eye. */}
        <div className="code__tot">
          <span className="code__totN">
            {edits.length} {edits.length === 1 ? 'file' : 'files'}
          </span>
          <span className="file__counts">
            <span className="add">+{total(edits, 'added')}</span>{' '}
            <span className="del">&minus;{total(edits, 'removed')}</span>
          </span>
        </div>
        <ul className="files">
        {edits.map((e, i) => (
          <li key={`${e.file}-${i}`}>
            <button
              className="file"
              aria-current={i === selected ? 'page' : undefined}
              onClick={() => setSelected(i)}
            >
              <span className="file__name">{basename(e.file)}</span>
              <span className="file__counts">
                <span className="add">+{e.added}</span> <span className="del">−{e.removed}</span>
              </span>
              <span className="file__path">{e.file}</span>
            </button>
          </li>
        ))}
        </ul>
      </div>

      <div className="diff">
        {!diff && <p className="diff__note">Reading snapshot…</p>}
        {/* A missing snapshot says why. The change still happened — pretending
            otherwise, or showing a blank pane, would both misrepresent it. */}
        {diff && !diff.found && <p className="diff__note">Diff unavailable — {diff.reason}</p>}
        {diff?.found && <DiffBody diff={diff} />}
        {current && diff?.found && (
          <ReviewBar
            file={current.file}
            outcome={acted[current.file]}
            busy={busy}
            onApprove={() => void act(current.file, 'approve')}
            onReject={() => void act(current.file, 'reject')}
          />
        )}
      </div>
    </div>
  )
}

function DiffBody({ diff }: { diff: Diff }) {
  return (
    <>
      <header className="diff__head">
        <span className="diff__file">{diff.file}</span>
        <span className="file__counts">
          <span className="add">+{diff.added}</span> <span className="del">−{diff.removed}</span>
        </span>
      </header>
      <pre className="diff__body">
        {diff.lines.map((l, i) => (
          <div key={i} className={`dl dl--${opClass(l.op)}`}>
            <span className="dl__n">{l.oldLine || ''}</span>
            <span className="dl__n">{l.newLine || ''}</span>
            <span className="dl__op">{l.op}</span>
            <span className="dl__text">
              <LineText line={l} />
            </span>
          </div>
        ))}
      </pre>
    </>
  )
}

function opClass(op: string): string {
  if (op === '+') return 'add'
  if (op === '-') return 'del'
  return 'ctx'
}

function basename(p: string): string {
  const i = Math.max(p.lastIndexOf('/'), p.lastIndexOf('\\'))
  return i < 0 ? p : p.slice(i + 1)
}

/**
 * A changed line, with the part that actually changed emphasised.
 *
 * The line's own colour says it changed; the spans say what on it moved, which
 * is the question a reviewer is actually asking. Without spans this is the
 * plain text it always was, so an added line, a removed line and a block
 * replacement all render exactly as before.
 */
function LineText({ line }: { line: DiffLine }) {
  const spans = line.spans
  if (!spans || spans.length === 0) return <>{line.text}</>

  const out: React.ReactNode[] = []
  let at = 0
  spans.forEach((s, i) => {
    // Defensive: a span outside the line would throw on slice. The backend
    // bounds these, but a stale bridge must degrade to plain text, not a
    // blank pane.
    if (s.start < at || s.end > line.text.length || s.start >= s.end) return
    if (s.start > at) out.push(line.text.slice(at, s.start))
    out.push(
      <mark className="dl__moved" key={i}>
        {line.text.slice(s.start, s.end)}
      </mark>,
    )
    at = s.end
  })
  if (at < line.text.length) out.push(line.text.slice(at))
  return <>{out}</>
}

/** Sums one count across every edit, so the header states a real total. */
export function total(edits: Edit[], key: 'added' | 'removed'): number {
  return edits.reduce((n, e) => n + (e[key] || 0), 0)
}

/**
 * Accept or undo the change on screen.
 *
 * Both actions are confirmed because neither is undoable from here: approving
 * drops the backup that made rolling back possible, and rejecting overwrites
 * the file. The confirm is inline rather than a dialog, so what is about to
 * happen stays next to the diff it applies to.
 */
function ReviewBar({
  file,
  outcome,
  busy,
  onApprove,
  onReject,
}: {
  file: string
  outcome?: ReviewOutcome
  busy: boolean
  onApprove: () => void
  onReject: () => void
}) {
  const [confirming, setConfirming] = useState<'approve' | 'reject' | null>(null)

  // Reset when the pane moves to a different file, or a confirm started on one
  // file would still be armed on the next.
  useEffect(() => setConfirming(null), [file])

  if (outcome?.error) {
    return (
      <div className="rev rev--err">
        <span className="rev__msg">{outcome.error}</span>
      </div>
    )
  }
  if (outcome?.status) {
    return (
      <div className={`rev rev--${outcome.status}`}>
        <span className="rev__msg">
          {outcome.status === 'approved' ? 'Accepted.' : 'Rolled back.'}
          {outcome.method && ` (${outcome.method.replace(/_/g, ' ')})`}
        </span>
        {/* Reviewing an edit is also the signal that trains routing, which is
            not obvious from a button that looks like a code-review control. */}
        <span className="rev__note">Recorded against the model that wrote it.</span>
      </div>
    )
  }

  if (confirming) {
    const approving = confirming === 'approve'
    return (
      <div className="rev rev--confirm">
        <span className="rev__msg">
          {approving
            ? 'Accept this change? The backup that would undo it is removed.'
            : 'Undo this change? The file goes back to its pre-edit contents.'}
        </span>
        <button
          className={approving ? 'rev__go' : 'rev__undo'}
          onClick={approving ? onApprove : onReject}
          disabled={busy}
        >
          {approving ? 'Accept' : 'Undo'}
        </button>
        <button className="rev__cancel" onClick={() => setConfirming(null)} disabled={busy}>
          Cancel
        </button>
      </div>
    )
  }

  return (
    <div className="rev">
      <button className="rev__go" onClick={() => setConfirming('approve')} disabled={busy}>
        Accept
      </button>
      <button className="rev__undo" onClick={() => setConfirming('reject')} disabled={busy}>
        Undo
      </button>
    </div>
  )
}
