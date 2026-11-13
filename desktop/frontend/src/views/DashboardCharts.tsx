import { useEffect, useMemo, useRef, useState } from 'react'
import type { Breakdown } from '../types'
import { costBand, usd } from '../format'

/* The chart lays out against its measured width rather than a fixed pitch.
   At 16px bars and an 8px gap, fourteen days filled about a third of a
   1900px container and the rest was empty (#1098). Bar width grows to fill
   and is clamped at both ends: too thin to see, and so wide that two days
   render as slabs, are both worse than a gap. */
const MIN_BAR = 6
const MAX_BAR = 44
const MIN_GAP = 5
/* Before the first measurement, so the first paint is not a zero-width chart
   that then jumps. Any plausible width will do; the observer corrects it. */
const ASSUMED_W = 900
const CHART_H = 160
// Reserves room above the tallest bar for the hover tooltip and today's
// figure, so neither clips off the top of the chart.
const TOP_PAD = 34

interface Bar {
  key: string
  x: number
  w: number
  barH: number
  costUsd: number
  calls: number
  band: ReturnType<typeof costBand>
}

/**
 * The dashboard's hero chart: one bar per day, height proportional to spend.
 * Same pattern as SessionGraph, a useMemo layout pass produces positions,
 * then plain SVG primitives render them, styled via tokens.css. Bars pick up
 * the existing cost ramp (free/cheap/mid/expensive) so an expensive day reads
 * as red without a legend.
 *
 * Drawn at its real height from the first frame, with today's figure written
 * above the last bar: a number to read, not a shape to estimate.
 */
export function SpendTrend({ days }: { days: Breakdown[] }) {
  const [hover, setHover] = useState<number | null>(null)
  const boxRef = useRef<HTMLDivElement>(null)
  const [width, setWidth] = useState(ASSUMED_W)

  // ResizeObserver is absent in jsdom, so the chart keeps the assumed width
  // under test rather than throwing; the layout is pure and testable either way.
  useEffect(() => {
    const el = boxRef.current
    if (!el || typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(([e]) => {
      const w = Math.round(e.contentRect.width)
      // Only a real change: a 0 during an unmount or a hidden tab would
      // collapse the chart and then animate it back on return.
      if (w > 0) setWidth(w)
    })
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  const bars = useMemo(() => layout(days, width), [days, width])
  const last = bars[bars.length - 1]

  const hovered = hover !== null ? bars[hover] : null

  return (
    <div className="spend-chart" ref={boxRef}>
      {bars.length > 0 && (
      <svg width={width} height={CHART_H} role="img" aria-label="Daily spend, most recent last">
        {bars.map((b, i) => (
          <g
            key={b.key}
            className="spend-bar"
            tabIndex={0}
            role="img"
            aria-label={`${b.key}: ${usd(b.costUsd)}, ${b.calls} call${b.calls === 1 ? '' : 's'}`}
            onMouseEnter={() => setHover(i)}
            onMouseLeave={() => setHover(null)}
            onFocus={() => setHover(i)}
            onBlur={() => setHover(null)}
          >
            <rect
              x={b.x}
              y={CHART_H - b.barH}
              width={b.w}
              height={b.barH}
              rx={2}
              className={`spend-bar__rect spend-bar__rect--${b.band}`}
            />
          </g>
        ))}
        {!hovered && last && (
          <text
            x={Math.min(last.x + last.w, width - 2)}
            y={Math.max(CHART_H - last.barH - 6, 12)}
            textAnchor="end"
            className="spend-chart__today"
          >
            {usd(last.costUsd)}
          </text>
        )}
        {hovered && <Tooltip bar={hovered} chartWidth={width} />}
      </svg>
      )}
      {bars.length > 0 && (
        <div className="spend-chart__range">
          <span>{bars[0].key}</span>
          {bars.length > 1 && <span>{bars[bars.length - 1].key}</span>}
        </div>
      )}
    </div>
  )
}

function Tooltip({ bar, chartWidth }: { bar: Bar; chartWidth: number }) {
  const w = 100
  const h = 32
  const x = Math.max(2, Math.min(bar.x + bar.w / 2 - w / 2, chartWidth - w - 2))
  const y = Math.max(CHART_H - bar.barH - h - 6, 2)
  return (
    <g className="spend-tip">
      <rect x={x} y={y} width={w} height={h} rx={6} className="spend-tip__bg" />
      <text x={x + 9} y={y + 14} className="spend-tip__value">
        {usd(bar.costUsd)}
      </text>
      <text x={x + 9} y={y + 26} className="spend-tip__meta">
        {bar.calls} call{bar.calls === 1 ? '' : 's'}
      </text>
    </g>
  )
}

/**
 * Lays the days out across `width`, which is the container's measured width.
 *
 * Pitch is the share each day gets; the bar takes as much of it as the clamps
 * allow and the gap absorbs the rest. Exported because it is the whole of the
 * sizing decision and is worth testing without a DOM: a chart that renders at
 * the wrong width is not something a render test notices.
 */
export function layout(days: Breakdown[], width: number): Bar[] {
  if (days.length === 0 || width <= 0) return []
  const max = Math.max(...days.map((d) => d.costUsd))
  const scale = max > 0 ? (CHART_H - TOP_PAD) / max : 0
  const pitch = width / days.length
  const w = Math.max(MIN_BAR, Math.min(MAX_BAR, pitch - MIN_GAP))
  return days.map((d, i) => ({
    key: d.key,
    // Centred in its own pitch, so the first and last bars are inset by half a
    // gap rather than flush against the edges.
    x: i * pitch + (pitch - w) / 2,
    w,
    // A zero-cost day (all local/free heads) still gets a visible nub rather
    // than vanishing, the day itself is real, even if it cost nothing.
    barH: max > 0 ? Math.max(2, d.costUsd * scale) : 2,
    costUsd: d.costUsd,
    calls: d.calls,
    band: costBand(d.costUsd, max),
  }))
}
