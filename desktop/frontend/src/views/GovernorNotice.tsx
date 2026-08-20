import { useEffect, useState } from 'react'
import type { GovernorPanel } from '../types'

/**
 * Fires when the orchestrator's context pressure reaches a band worth
 * interrupting for, and says how much headroom is left rather than only how
 * full it is.
 *
 * Once per band, never mid-answer: an interruption that repeats on every poll
 * is noise, and one that lands mid-dispatch cannot be acted on anyway.
 *
 * The numbers are budget.RiskFromHistory's own — burn rate in percentage points
 * per observation, and the first-passage probability of reaching 80% within
 * budget.RiskHorizon observations. An observation is a claude_pct *update*, not
 * a wall-clock interval and not a chat turn, so the copy says "updates".
 */
export function GovernorNotice({
  governor,
  busy,
}: {
  governor: GovernorPanel | undefined
  busy: boolean
}) {
  const [dismissed, setDismissed] = useState<string[]>([])

  const band = governor?.known ? governor.effectiveMode : ''
  const loud = band === 'critical' || band === 'emergency'
  const show = loud && !busy && !dismissed.includes(band)

  useEffect(() => {
    if (!show) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setDismissed((d) => [...d, band])
    }
    addEventListener('keydown', onKey)
    return () => removeEventListener('keydown', onKey)
  }, [show, band])

  if (!show || !governor) return null

  const g = governor
  // Only claimed when there is a real rate signal: RiskFromHistory returns zero
  // below two observations, and presenting that as "no risk" would be a lie.
  const hasRate = g.observations >= 2 && g.burnRatePct > 0
  const headroom = hasRate ? Math.max(0, Math.ceil((80 - g.pct) / g.burnRatePct)) : 0

  return (
    <div className="gn" role="dialog" aria-modal="true" aria-labelledby="gnTitle">
      <div className="gn__box">
        <div className="gn__stripe" />
        <div className="gn__in">
          <span className={`gn__band gn__band--${band}`}>
            {band} · {g.pct}%
          </span>
          <h3 className="gn__h" id="gnTitle">
            {band === 'emergency'
              ? 'Context is effectively full'
              : 'Running out of orchestrator context'}
          </h3>

          {hasRate ? (
            <p className="gn__p">
              Burning <b>{g.burnRatePct.toFixed(1)}pp</b> per update. At that rate the 80%
              ceiling is about <b>{headroom} update{headroom === 1 ? '' : 's'}</b> away
              {g.risk > 0 && (
                <>
                  {' '}
                  — a <b>{pctText(g.risk)}</b> chance of reaching it within the next{' '}
                  {g.horizonObs} update{g.horizonObs === 1 ? '' : 's'}
                </>
              )}
              .
            </p>
          ) : (
            <p className="gn__p">
              At <b>{g.pct}%</b> of the context window. Not enough history yet to estimate
              a burn rate, so this is the level alone.
            </p>
          )}

          <div className="gn__meter">
            <div className="gn__fill" style={{ width: `${Math.min(100, g.pct)}%` }} />
            <div className="gn__ceil" />
            <span className="gn__now">{g.pct}% now</span>
            <span className="gn__stop">80%</span>
          </div>

          <p className="gn__note">
            Past 80% Hydra routes to local heads only. Running <code>/compact</code> in the
            orchestrator session is what actually clears it.
          </p>

          {g.effectiveMode !== g.mode && (
            <p className="gn__esc">
              Raised from <b>{g.mode}</b> by how fast this is climbing, not by the level
              alone.
            </p>
          )}

          <div className="gn__acts">
            <button className="gn__btn" onClick={() => setDismissed((d) => [...d, band])}>
              Got it
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}

function pctText(p: number): string {
  const v = Math.round(p * 100)
  // Never round a real probability to a flat 0% — "<1%" is honest, "0%" is not.
  if (v === 0) return '<1%'
  return `${v}%`
}
