import { useEffect, useMemo, useRef, useState } from 'react'

export interface Command {
  id: string
  /** What the row reads as. */
  label: string
  /** The group heading this row sorts under. */
  group: string
  /** Right-aligned detail: a tier, a cost, a run id. */
  hint?: string
  run: () => void
}

interface Props {
  open: boolean
  onClose: () => void
  commands: Command[]
}

/**
 * The header's search box, made real. A box that opened nothing would be worse
 * than no box: it advertises a capability the app does not have.
 *
 * Matching is subsequence, not substring, so "acs" finds
 * "anthropic/claude-sonnet" the way every palette people already use behaves.
 */
export function CommandPalette({ open, onClose, commands }: Props) {
  const [q, setQ] = useState('')
  const [sel, setSel] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const listRef = useRef<HTMLDivElement>(null)

  const hits = useMemo(() => {
    const scored = commands
      .map((c) => ({ c, s: score(c.label, q) }))
      .filter((x) => x.s > 0)
      .sort((a, b) => b.s - a.s)
    return scored.slice(0, 40).map((x) => x.c)
  }, [commands, q])

  // Reopening must not inherit the last query, and a new query must not keep a
  // selection index that now points at a different command.
  useEffect(() => {
    if (open) {
      setQ('')
      setSel(0)
      inputRef.current?.focus()
    }
  }, [open])
  useEffect(() => setSel(0), [q])

  // Keep the active row in view when arrowing past the fold.
  useEffect(() => {
    const el = listRef.current?.querySelector('[data-sel="true"]')
    el?.scrollIntoView({ block: 'nearest' })
  }, [sel, hits])

  if (!open) return null

  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === 'Escape') return onClose()
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setSel((n) => Math.min(n + 1, hits.length - 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setSel((n) => Math.max(n - 1, 0))
    } else if (e.key === 'Enter') {
      e.preventDefault()
      const c = hits[sel]
      if (c) {
        c.run()
        onClose()
      }
    }
  }

  let lastGroup = ''
  return (
    <div className="palette__scrim" onMouseDown={onClose} role="presentation">
      <div
        className="palette"
        role="dialog"
        aria-modal="true"
        aria-label="Search"
        onMouseDown={(e) => e.stopPropagation()}
      >
        <input
          ref={inputRef}
          className="palette__input"
          value={q}
          placeholder="Search models, runs, views…"
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={onKey}
          aria-label="Search"
        />
        <div className="palette__list" ref={listRef}>
          {hits.length === 0 && <p className="palette__empty">Nothing matches “{q}”.</p>}
          {hits.map((c, i) => {
            const head = c.group !== lastGroup ? c.group : ''
            lastGroup = c.group
            return (
              <div key={c.id}>
                {head && <div className="palette__group">{head}</div>}
                <button
                  className="palette__row"
                  data-sel={i === sel ? 'true' : undefined}
                  onMouseEnter={() => setSel(i)}
                  onClick={() => {
                    c.run()
                    onClose()
                  }}
                >
                  <span className="palette__label">{c.label}</span>
                  {c.hint && <span className="palette__hint">{c.hint}</span>}
                </button>
              </div>
            )
          })}
        </div>
        <div className="palette__foot">
          <kbd>↑</kbd>
          <kbd>↓</kbd> to move <kbd>↵</kbd> to open <kbd>esc</kbd> to close
        </div>
      </div>
    </div>
  )
}

/**
 * Subsequence match, scored so that earlier and more contiguous runs win. An
 * empty query matches everything at equal weight, which is what makes the
 * palette useful as a plain menu before anyone types.
 */
export function score(label: string, q: string): number {
  if (!q) return 1
  const l = label.toLowerCase()
  const needle = q.toLowerCase().replace(/\s+/g, '')
  let i = 0
  let s = 0
  let streak = 0
  for (let j = 0; j < l.length && i < needle.length; j++) {
    if (l[j] === needle[i]) {
      streak++
      // Contiguity is worth more than position, so "sonnet" beats a scatter of
      // the same six letters spread across the whole id.
      s += 1 + streak * 2 + (j === 0 ? 5 : 0)
      i++
    } else {
      streak = 0
    }
  }
  return i === needle.length ? s : 0
}
