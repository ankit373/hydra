import { useEffect, useState } from 'react'
import { GetDiff } from '../bindings'
import type { Diff, DiffLine, Edit } from '../types'

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

  // Re-selects on every new value, not just at mount — Session stays mounted
  // across clicking different artifact nodes in the same run, so this has to
  // react to initialFile changing, not just seed the initial state.
  useEffect(() => {
    if (!initialFile) return
    const i = edits.findIndex((e) => e.file === initialFile)
    if (i >= 0) setSelected(i)
  }, [initialFile, edits])

  const current = edits[selected]

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
