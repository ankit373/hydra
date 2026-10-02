import type { Version } from '../types'
import { usd } from '../format'

interface Props {
  version: Version | null
  /** Null while the first probe is still running. */
  routable: number | null
  total: number | null
  local: number | null
  todayUsd: number | null
  calls: number | null
  mode: string
}

/**
 * The status bar. Everything here is a fact the app already read for another
 * reason, so the footer costs no extra call; it just stops those facts being
 * three clicks away.
 */
export function AppFooter({ version, routable, total, local, todayUsd, calls, mode }: Props) {
  return (
    <footer className="appfoot">
      <span className="appfoot__item appfoot__item--ver" title={version?.commit ?? ''}>
        {version ? version.version : '—'}
        {version?.commit && <span className="appfoot__sub"> · {version.commit.slice(0, 7)}</span>}
      </span>

      <span className="appfoot__sep" aria-hidden="true" />

      <span className="appfoot__item">
        {routable === null || total === null ? (
          'probing heads…'
        ) : (
          <>
            <strong>{routable}</strong> of {total} heads routable
            {local !== null && local > 0 && <span className="appfoot__sub"> · {local} local</span>}
          </>
        )}
      </span>

      <span className="appfoot__grow" />

      {mode && (
        <span className={`appfoot__mode appfoot__mode--${mode}`} title="Context budget mode">
          {mode}
        </span>
      )}

      <span className="appfoot__item">
        {todayUsd === null ? (
          '—'
        ) : (
          <>
            <strong>{usd(todayUsd)}</strong> today
            {calls !== null && <span className="appfoot__sub"> · {calls} calls</span>}
          </>
        )}
      </span>
    </footer>
  )
}
