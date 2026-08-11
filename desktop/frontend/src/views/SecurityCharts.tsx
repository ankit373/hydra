import type { Category, CoverageStatus, HistoryPoint } from '../types'

/**
 * The Security hero's three bespoke SVG charts, following DashboardCharts'
 * house style: a useMemo-free layout pass (these are cheap enough to compute
 * inline) followed by plain SVG primitives, styled through app.css/tokens.css
 * — no charting library.
 */

const RING_R = 54
const RING_STROKE = 10
const RING_SIZE = (RING_R + RING_STROKE) * 2
const RING_C = 2 * Math.PI * RING_R

/** The coverage percentage as a progress ring — the hero's single glanceable
 * number, big enough to read in five seconds. */
export function CoverageRing({ percent, band }: { percent: number; band: 'good' | 'warn' | 'bad' }) {
  const clamped = Math.max(0, Math.min(100, percent))
  const dash = (clamped / 100) * RING_C
  const c = RING_SIZE / 2
  return (
    <svg
      className="sec-ring"
      width={RING_SIZE}
      height={RING_SIZE}
      viewBox={`0 0 ${RING_SIZE} ${RING_SIZE}`}
      role="img"
      aria-label={`${Math.round(clamped)} percent OWASP LLM Top-10 coverage`}
    >
      <circle className="sec-ring__track" cx={c} cy={c} r={RING_R} strokeWidth={RING_STROKE} fill="none" />
      <circle
        className={`sec-ring__fill sec-ring__fill--${band}`}
        cx={c}
        cy={c}
        r={RING_R}
        strokeWidth={RING_STROKE}
        fill="none"
        strokeDasharray={`${dash} ${RING_C}`}
        strokeLinecap="round"
        transform={`rotate(-90 ${c} ${c})`}
      />
      <text x={c} y={c} textAnchor="middle" dominantBaseline="central" className="sec-ring__label">
        {Math.round(clamped)}%
      </text>
    </svg>
  )
}

const HIST_W = 260
const HIST_H = 64

/** The real coverage series, oldest to newest — fixed 0-100 scale (not
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
      role="img"
      aria-label={`Coverage over ${history.length} recorded runs, most recent ${latestPct} percent`}
    >
      <path className="sec-history__area" d={area} />
      <polyline className="sec-history__line" points={line} fill="none" />
      <circle className="sec-history__dot" cx={last[0]} cy={last[1]} r={3} />
    </svg>
  )
}

const RADAR_SIZE = 220
const RADAR_R = 78

function statusValue(status: CoverageStatus): number {
  switch (status) {
    case 'enforced':
      return 1
    case 'configured':
      return 2 / 3
    case 'gap':
      return 1 / 3
    default:
      return 0
  }
}

/** The "shape" of coverage across applicable categories, NIST-CSF-radar
 * style: enforced reaches the rim, configured two-thirds out, a gap only a
 * third — a dent in the polygon is a gap, at a glance, no legend needed to
 * see *that* something is missing (the category list still names what). */
export function CoverageRadar({ categories }: { categories: Category[] }) {
  const cats = categories.filter((c) => c.status !== 'n/a')
  const n = cats.length
  if (n < 3) return null // fewer than 3 axes doesn't read as a shape

  const c = RADAR_SIZE / 2
  const angle = (i: number) => (Math.PI * 2 * i) / n - Math.PI / 2
  const pointAt = (i: number, value: number): readonly [number, number] => {
    const r = RADAR_R * value
    return [c + r * Math.cos(angle(i)), c + r * Math.sin(angle(i))]
  }
  const shape = cats.map((cat, i) => pointAt(i, statusValue(cat.status)))

  return (
    <svg
      className="sec-radar"
      width={RADAR_SIZE}
      height={RADAR_SIZE}
      viewBox={`0 0 ${RADAR_SIZE} ${RADAR_SIZE}`}
      role="img"
      aria-label="Coverage shape across the OWASP LLM Top-10 applicable categories"
    >
      {[1, 2 / 3, 1 / 3].map((ring) => (
        <polygon
          key={ring}
          className="sec-radar__ring"
          points={cats.map((_, i) => pointAt(i, ring).join(',')).join(' ')}
          fill="none"
        />
      ))}
      {cats.map((cat, i) => {
        const [x, y] = pointAt(i, 1)
        return <line key={cat.id} className="sec-radar__axis" x1={c} y1={c} x2={x} y2={y} />
      })}
      <polygon className="sec-radar__shape" points={shape.map((p) => p.join(',')).join(' ')} />
      {cats.map((cat, i) => {
        const [x, y] = pointAt(i, 1.18)
        return (
          <text key={cat.id} x={x} y={y} textAnchor="middle" className="sec-radar__label">
            {cat.id.replace('LLM', '')}
          </text>
        )
      })}
    </svg>
  )
}
