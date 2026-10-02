import { HydraMark } from '../brand'
import { UpdateNotice } from './UpdateNotice'
import { usd } from '../format'

export interface NavItem {
  id: string
  label: string
  /** Unread-style count rendered on the tab. Zero renders nothing. */
  badge?: number
  /** True when the badge counts live work rather than work waiting on a human. */
  live?: boolean
}

interface Props {
  nav: readonly NavItem[]
  current: string
  onSelect: (id: string) => void
  onSearch: () => void
  /** Null until the first probe answers, so "no heads" never renders as a fact
   *  before anything has looked. */
  routable: number | null
  todayUsd: number | null
  onHeads: () => void
  onSpend: () => void
}

/**
 * The window's title bar. main.go runs mac.TitleBarHiddenInset(), so the OS
 * draws no bar and nothing stood in its place: the traffic lights floated over
 * an empty rail. This is also the drag region.
 */
export function AppHeader({
  nav,
  current,
  onSelect,
  onSearch,
  routable,
  todayUsd,
  onHeads,
  onSpend,
}: Props) {
  return (
    <header className="apphead">
      <div className="apphead__brand">
        <HydraMark className="apphead__mark" />
        <span className="apphead__word">HYDRA</span>
      </div>

      <button className="apphead__search" onClick={onSearch} title="Search (⌘K)">
        <span className="apphead__searchicon" aria-hidden="true">
          ⌕
        </span>
        <span className="apphead__searchtext">Search models, runs, views…</span>
        <kbd className="apphead__kbd">⌘K</kbd>
      </button>

      <nav className="apphead__nav" aria-label="Views">
        {nav.map((n) => (
          <button
            key={n.id}
            className="apphead__tab"
            aria-current={current === n.id ? 'page' : undefined}
            onClick={() => onSelect(n.id)}
          >
            {n.label}
            {!!n.badge && (
              <span className={n.live ? 'apphead__badge apphead__badge--live' : 'apphead__badge'}>
                {n.badge}
              </span>
            )}
          </button>
        ))}
      </nav>

      <div className="apphead__status">
        {/* Chrome, not content: it used to sit in the rail footer, and after
            the rail went it rendered loose above the view (#1060). */}
        <UpdateNotice />
        <button className="apphead__stat" onClick={onHeads} title="What this machine can route to">
          <span
            className={
              routable === null
                ? 'apphead__dot apphead__dot--unknown'
                : routable > 0
                  ? 'apphead__dot apphead__dot--ok'
                  : 'apphead__dot apphead__dot--none'
            }
            aria-hidden="true"
          />
          {/* An unread probe is not zero heads: one is "nothing looked yet". */}
          {routable === null ? 'probing…' : `${routable} routable`}
        </button>
        <button className="apphead__stat" onClick={onSpend} title="What you spent today">
          {todayUsd === null ? '—' : usd(todayUsd)}
        </button>
      </div>
    </header>
  )
}
