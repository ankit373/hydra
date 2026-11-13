import type { Category, CoverageStatus, DayRisk, HistoryPoint } from '../types'

/**
 * The Security hero's small SVG charts, following DashboardCharts'
 * house style: a useMemo-free layout pass (these are cheap enough to compute
 * inline) followed by plain SVG primitives, styled through app.css/tokens.css
 *, no charting library.
 */

const HIST_W = 260
const HIST_H = 64

/** The real coverage series, oldest to newest, fixed 0-100 scale (not
 * data-relative like the spend chart) so "75%" always reads the same height,
 * run to run. */
export function CoverageHistory({ history }: { history: HistoryPoint[] }) {
  if (history.length < 2) return null
  const points = history.map((h, i) => {
    const x = (i / (history.length - 1)) * HIST_W
    const y = HIST_H - (Math.max(0, Math.min(100, h.percentCovered)) / 100) * HIST_H
    return [x, y] as const
  })
  const last = points[points.length - 1]
  const latestPct = Math.round(history[history.length - 1].percentCovered)
  const line = points.map(([x, y]) => `${x},${y}`).join(' ')
  const area = `M0,${HIST_H} L${line} L${HIST_W},${HIST_H} Z`

  return (
    <svg
      className="sec-history"
      width={HIST_W}
      height={HIST_H}
      viewBox={`0 0 ${HIST_W} ${HIST_H}`}
      role="img"
      aria-label={`Coverage over ${history.length} recorded runs, most recent ${latestPct} percent`}
    >
      {/* A flat series with no reference line reads as a filled progress bar
          rather than a chart. The 100% rule gives the level something to sit against. */}
      <line className="sec-history__grid" x1={0} y1={0.5} x2={HIST_W} y2={0.5} />
      <path className="sec-history__area" d={area} />
      <polyline className="sec-history__line" points={line} fill="none" />
      <circle className="sec-history__dot" cx={last[0]} cy={last[1]} r={3} />
    </svg>
  )
}

const STATUS_FILL: Record<CoverageStatus, number> = {
  enforced: 1,
  configured: 2 / 3,
  partial: 1 / 2, // detective, not preventive: real, and short of configured
  gap: 1 / 3,
  'n/a': 0,
}

/**
 * A small-multiples grid replacing an earlier radar/spider chart: radar
 * charts are a well-documented legibility trap (axis order changes the shape
 * you see, precise values are hard to read, more than 2-3 series turns
 * illegible), a grid of small, independently-labelled tiles is the standard
 * alternative, and each tile is self-explanatory without a legend: the ID,
 * a fill bar (a third/two-thirds/full by status), and the status word.
 */
export function CategoryGrid({ categories }: { categories: Category[] }) {
  const cats = categories.filter((c) => c.status !== 'n/a')
  if (cats.length === 0) return null
  return (
    <div className="sec-grid">
      {cats.map((c) => (
        <div key={c.id} className={`sec-tile sec-tile--${c.status}`} title={c.name}>
          <div className="sec-tile__id">{c.id}</div>
          <div className="sec-tile__bar">
            <div className="sec-tile__fill" style={{ width: `${STATUS_FILL[c.status] * 100}%` }} />
          </div>
          <div className="sec-tile__status">{c.status}</div>
        </div>
      ))}
    </div>
  )
}

const RISK_W = 260
const RISK_H = 64

/**
 * Denied (blocked) and flagged (suspected injection marker) accesses per day
 *, the "blocked over time" trend WAF dashboards report as their headline
 * security KPI. Scaled to its own data (unlike CoverageHistory's fixed
 * 0-100), since these are small, unbounded counts, not a percentage.
 */
export function RiskTrend({ history }: { history: DayRisk[] }) {
  if (history.length < 2) return null
  const max = Math.max(1, ...history.map((d) => Math.max(d.denied, d.flagged)))
  const xAt = (i: number) => (i / (history.length - 1)) * RISK_W
  const yAt = (v: number) => RISK_H - (Math.min(v, max) / max) * RISK_H
  const toLine = (pts: (readonly [number, number])[]) => pts.map(([x, y]) => `${x},${y}`).join(' ')
  const deniedLine = toLine(history.map((d, i) => [xAt(i), yAt(d.denied)] as const))
  const flaggedLine = toLine(history.map((d, i) => [xAt(i), yAt(d.flagged)] as const))

  // Two series, so a legend: the lines alone said which was which by colour only.
  const key = (cls: string, label: string) => (
    <span>
      <svg width="16" height="6" aria-hidden="true">
        <line className={cls} x1="0" y1="3" x2="16" y2="3" />
      </svg>
      {label}
    </span>
  )
  return (
    <>
      <svg
        className="sec-risktrend"
        width={RISK_W}
        height={RISK_H}
        viewBox={`0 0 ${RISK_W} ${RISK_H}`}
        role="img"
        aria-label={`Denied and flagged accesses over ${history.length} days`}
      >
        <polyline className="sec-risktrend__denied" points={deniedLine} fill="none" />
        <polyline className="sec-risktrend__flagged" points={flaggedLine} fill="none" />
      </svg>
      <div className="sec-risktrend-legend" aria-hidden="true">
        {key('sec-risktrend__denied', 'denied')}
        {key('sec-risktrend__flagged', 'flagged')}
      </div>
    </>
  )
}
