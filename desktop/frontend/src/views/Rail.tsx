import type { Version } from '../types'
import { usd } from '../format'
import { UpdateNotice } from './UpdateNotice'
import { ThemeToggle } from './ThemeToggle'

export interface NavItem {
  id: string
  label: string
  /** Short text beside the label, such as "1 waiting". Empty renders nothing. */
  note?: string
  /** True when the note is about work that needs a person, not work in flight. */
  urgent?: boolean
}

interface Props {
  nav: readonly NavItem[]
  current: string
  onSelect: (id: string) => void
  onSearch: () => void
  /** Null until the first probe answers, so "no models" is never shown before
   *  anything has looked. */
  routable: number | null
  total: number | null
  /** Why the last probe did not answer. A failed probe is not one in flight. */
  headsError: string | null
  todayUsd: number | null
  calls: number | null
  /** The context budget band, empty when nothing has reported one. */
  mode: string
  version: Version | null
  onHeads: () => void
  onSpend: () => void
}

/**
 * Navigation down the left edge, so the main column keeps the full height and
 * Chat keeps its width. It also replaces the header and the status bar: the
 * facts they held (models ready, spend today, the context band, the version)
 * sit at the foot of the rail.
 *
 * main.go runs mac.TitleBarHiddenInset(), so the top of the rail is the drag
 * region and leaves room for the traffic lights.
 */
export function Rail({
  nav,
  current,
  onSelect,
  onSearch,
  routable,
  total,
  headsError,
  todayUsd,
  calls,
  mode,
  version,
  onHeads,
  onSpend,
}: Props) {
  return (
    <aside className="rail" aria-label="Hydra">
      <div className="rail__top">
        <span className="rail__word">hydra</span>
      </div>

      <button className="rail__search" onClick={onSearch} title="Search models, runs and views">
        <span>Search</span>
        <kbd className="rail__kbd">⌘K</kbd>
      </button>

      <nav className="rail__nav" aria-label="Views">
        {nav.map((n) => (
          <button
            key={n.id}
            className="rail__item"
            aria-current={current === n.id ? 'page' : undefined}
            onClick={() => onSelect(n.id)}
          >
            <span className="rail__label">{n.label}</span>
            {n.note && (
              <span className={n.urgent ? 'rail__note rail__note--urgent' : 'rail__note'}>
                {n.note}
              </span>
            )}
          </button>
        ))}
      </nav>

      <div className="rail__grow" />

      <UpdateNotice />

      <div className="rail__facts">
        <button className="rail__fact" onClick={onHeads} title={headsError || 'Models this machine can use'}>
          {/* Three states: an unread probe is not zero models, and a probe
              that failed is not one still running. */}
          {routable !== null && total !== null ? (
            <>
              <span className="rail__num">{routable}</span> of {total} models ready
            </>
          ) : headsError ? (
            'probe failed'
          ) : (
            'looking for models…'
          )}
        </button>
        <button className="rail__fact" onClick={onSpend} title="What you spent today">
          {todayUsd === null ? (
            'spend not read yet'
          ) : (
            <>
              <span className="rail__num">{usd(todayUsd)}</span> today
              {calls !== null && <span className="rail__sub">{calls} requests</span>}
            </>
          )}
        </button>
        {mode && (
          <p className={`rail__mode rail__mode--${mode}`} title="Context budget band">
            Context: {mode}
          </p>
        )}
      </div>

      <div className="rail__foot">
        <span className="rail__ver" title={version?.commit ?? ''}>
          {version ? version.version : ''}
        </span>
        <ThemeToggle />
      </div>
    </aside>
  )
}
