import { useId } from 'react'

export interface Tab<T extends string> {
  id: T
  label: string
  /** Rendered as a pill beside the label. Undefined renders nothing, which is
   *  different from zero: Models has real counts, Session has none. */
  count?: number
}

interface Props<T extends string> {
  tabs: readonly Tab<T>[]
  current: T
  onSelect: (id: T) => void
  label: string
  /** The id of the element these tabs switch, so the roles are not a claim the
   *  markup fails to honour. */
  panelID: string
}

/**
 * The one tab strip. There were two: `.tab` pills in Session and Audit, and the
 * underlined `.tabs__tab` added for Models, whose container rule also set a
 * border-bottom on the class the pills already used, so Models' styling leaked
 * onto both of them (#1098).
 *
 * `role="tab"` is a promise about what the thing controls, so `aria-controls`
 * and a matching `tabpanel` are not decoration: without them the roles say
 * something the markup does not do.
 */
export function Tabs<T extends string>({ tabs, current, onSelect, label, panelID }: Props<T>) {
  const base = useId()
  return (
    <div className="tabs" role="tablist" aria-label={label}>
      {tabs.map((t) => (
        <button
          key={t.id}
          id={`${base}-${t.id}`}
          role="tab"
          className="tabs__tab"
          aria-selected={current === t.id}
          aria-controls={panelID}
          // Roving tabindex: a tablist is one stop, arrowed within.
          tabIndex={current === t.id ? 0 : -1}
          onClick={() => onSelect(t.id)}
          onKeyDown={(e) => {
            if (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft') return
            e.preventDefault()
            const i = tabs.findIndex((x) => x.id === current)
            const next = e.key === 'ArrowRight' ? i + 1 : i - 1
            // Wraps, which is what a tablist does and what makes the last tab
            // reachable from the first without arrowing back through all of them.
            onSelect(tabs[(next + tabs.length) % tabs.length].id)
          }}
        >
          {t.label}
          {t.count !== undefined && <span className="tabs__n">{t.count}</span>}
        </button>
      ))}
    </div>
  )
}
