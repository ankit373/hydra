import { useEffect, useState } from 'react'
import { GetDiff } from '../bindings'
import type { Diff, Edit } from '../types'

export function Code({ runID, edits }: { runID: string; edits: Edit[] }) {
  const [selected, setSelected] = useState(0)
  const [diff, setDiff] = useState<Diff | null>(null)

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
            <span className="dl__text">{l.text}</span>
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
