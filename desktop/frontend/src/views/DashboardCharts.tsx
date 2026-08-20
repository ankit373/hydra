import { useMemo, useState } from 'react'
import type { Breakdown } from '../types'
import { costBand, usd } from '../format'
import { useReveal } from '../reveal'

const BAR_W = 16
const GAP = 8
const CHART_H = 160
// Reserves room above the tallest bar for the hover tooltip, so it never
// clips off the top of the chart.
const TOP_PAD = 34
// How far the halo extends past the crisp bar it sits behind, on each edge.
const HALO_PAD = 3

interface Bar {
  key: string
  x: number
  barH: number
  costUsd: number
  calls: number
  band: ReturnType<typeof costBand>
}

/**
 * The dashboard's hero chart: one bar per day, height proportional to spend.
 * Same pattern as SessionGraph — a useMemo layout pass produces positions,
 * then plain SVG primitives render them, styled via tokens.css. Bars pick up
 * the existing cost ramp (free/cheap/mid/expensive) so an expensive day reads
 * as red without a legend.
 *
 * Each bar is drawn as a halo (larger, low-opacity, same band color) behind a
 * crisp full-opacity rect — a layered solid fill, never `filter: blur` or
 * `backdrop-filter`, so the glow rasterizes like any other shape instead of
 * costing a compositor blur pass. Bars grow in from a zero baseline
 * (`transform: scaleY`) the first time real data lands, staggered per bar;
 * `useReveal` makes that fire once per arrival, not on every 5s poll tick.
 */
export function SpendTrend({ days }: { days: Breakdown[] }) {
  const [hover, setHover] = useState<number | null>(null)
  const { bars, width } = useMemo(() => layout(days), [days])
  const revealed = useReveal(bars.length > 0)

  if (bars.length === 0) return null

  const hovered = hover !== null ? bars[hover] : null

  return (
    <div className="spend-chart">
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
              x={b.x - HALO_PAD}
              y={CHART_H - b.barH - HALO_PAD}
              width={BAR_W + HALO_PAD * 2}
              height={b.barH + HALO_PAD}
              rx={5}
              className={`spend-bar__halo spend-bar__halo--${b.band}${revealed ? ' grown' : ''}`}
              style={revealed ? { transitionDelay: `${Math.min(i * 12, 220)}ms` } : undefined}
            />
            <rect
              x={b.x}
              y={CHART_H - b.barH}
              width={BAR_W}
              height={b.barH}
              rx={2}
              className={`spend-bar__rect spend-bar__rect--${b.band}${revealed ? ' grown' : ''}`}
              style={revealed ? { transitionDelay: `${Math.min(i * 12, 220)}ms` } : undefined}
            />
          </g>
        ))}
        {hovered && <Tooltip bar={hovered} chartWidth={width} />}
      </svg>
      <div className="spend-chart__range">
        <span>{bars[0].key}</span>
        {bars.length > 1 && <span>{bars[bars.length - 1].key}</span>}
      </div>
    </div>
  )
}

function Tooltip({ bar, chartWidth }: { bar: Bar; chartWidth: number }) {
  const w = 100
  const h = 32
  const x = Math.max(2, Math.min(bar.x + BAR_W / 2 - w / 2, chartWidth - w - 2))
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

function layout(days: Breakdown[]): { bars: Bar[]; width: number } {
  if (days.length === 0) return { bars: [], width: 0 }
  const max = Math.max(...days.map((d) => d.costUsd))
  const scale = max > 0 ? (CHART_H - TOP_PAD) / max : 0
  const bars = days.map((d, i) => ({
    key: d.key,
    x: i * (BAR_W + GAP),
    // A zero-cost day (all local/free heads) still gets a visible nub rather
    // than vanishing — the day itself is real, even if it cost nothing.
    barH: max > 0 ? Math.max(2, d.costUsd * scale) : 2,
    costUsd: d.costUsd,
    calls: d.calls,
    band: costBand(d.costUsd, max),
  }))
  return { bars, width: days.length * (BAR_W + GAP) - GAP }
}

const SPARK_W = 96
const SPARK_H = 26

/**
 * Inline trend inside the spend stat card: the tail of byDay, so "is this
 * normal" is visible without scrolling down to the hero chart. Follows the
 * sparkline figure spec — the line stays in the de-emphasis hue, only the
 * current (latest) point picks up the accent.
 */
export function Sparkline({ days }: { days: Breakdown[] }) {
  const tail = days.slice(-14)
  if (tail.length < 2) return null

  const max = Math.max(...tail.map((d) => d.costUsd), 0)
  const points = tail.map((d, i) => {
    const x = (i / (tail.length - 1)) * SPARK_W
    const y = max > 0 ? SPARK_H - (d.costUsd / max) * SPARK_H : SPARK_H
    return [x, y] as const
  })
  const last = points[points.length - 1]

  return (
    <svg
      className="spend-spark"
      width={SPARK_W}
      height={SPARK_H}
      role="img"
      aria-label={`Spend trend over the last ${tail.length} days`}
    >
      <polyline
        className="spend-spark__line"
        points={points.map(([x, y]) => `${x},${y}`).join(' ')}
        fill="none"
      />
      <circle className="spend-spark__dot" cx={last[0]} cy={last[1]} r={2.5} />
    </svg>
  )
}

// ── arc gauges ───────────────────────────────────────────────────────────
// Replace the flat progress bars on GovernorCard/TrustCard with SVG rings:
// an outer track, and a fill ring whose stroke-dasharray/dashoffset encode
// the fraction — animated purely in CSS (tokens.css's `.arc-fill` transition)
// so the sweep-in is a single reflow-free property change, not a JS loop.

const ARC_SIZE = 112
const ARC_CENTER = ARC_SIZE / 2
const ARC_R = 44
const ARC_STROKE = 8
const ARC_R_INNER = 33
const ARC_STROKE_INNER = 5

function arcDash(r: number, frac: number): { circumference: number; offset: number } {
  const circumference = 2 * Math.PI * r
  const clamped = Math.max(0, Math.min(1, frac))
  return { circumference, offset: circumference * (1 - clamped) }
}

/** Single-ring gauge — Governor's context-window pressure. */
export function ArcGauge({
  fraction,
  color,
  centerValue,
  centerLabel,
  revealed,
}: {
  /** 0–1, already clamped by the caller. */
  fraction: number
  color: string
  centerValue: string
  centerLabel: string
  revealed: boolean
}) {
  const { circumference, offset } = arcDash(ARC_R, revealed ? fraction : 0)
  return (
    <svg
      className="arc-gauge"
      width={ARC_SIZE}
      height={ARC_SIZE}
      viewBox={`0 0 ${ARC_SIZE} ${ARC_SIZE}`}
      role="img"
      aria-label={`${centerLabel}: ${centerValue}`}
    >
      <circle className="arc-track" cx={ARC_CENTER} cy={ARC_CENTER} r={ARC_R} strokeWidth={ARC_STROKE} />
      <circle
        className="arc-fill"
        cx={ARC_CENTER}
        cy={ARC_CENTER}
        r={ARC_R}
        strokeWidth={ARC_STROKE}
        stroke={color}
        strokeDasharray={circumference}
        strokeDashoffset={offset}
        transform={`rotate(-90 ${ARC_CENTER} ${ARC_CENTER})`}
      />
      <text x={ARC_CENTER} y={ARC_CENTER - 3} textAnchor="middle" className="arc-num">
        {centerValue}
      </text>
      <text x={ARC_CENTER} y={ARC_CENTER + 14} textAnchor="middle" className="arc-lbl">
        {centerLabel}
      </text>
    </svg>
  )
}

/** Dual-ring gauge — SPRT mean samples (outer, aqua) vs. a fixed swarm (inner, dim), both scaled to the larger of the two so the comparison reads directly off the rings. */
export function TrustArc({
  meanSamples,
  fixedSwarmN,
  revealed,
}: {
  meanSamples: number
  fixedSwarmN: number
  revealed: boolean
}) {
  const max = Math.max(meanSamples, fixedSwarmN, 1)
  const outer = arcDash(ARC_R, revealed ? meanSamples / max : 0)
  const inner = arcDash(ARC_R_INNER, revealed ? fixedSwarmN / max : 0)
  return (
    <svg
      className="arc-gauge"
      width={ARC_SIZE}
      height={ARC_SIZE}
      viewBox={`0 0 ${ARC_SIZE} ${ARC_SIZE}`}
      role="img"
      aria-label={`${meanSamples.toFixed(2)} samples asked on average, versus a fixed panel of ${fixedSwarmN}`}
    >
      <circle className="arc-track" cx={ARC_CENTER} cy={ARC_CENTER} r={ARC_R} strokeWidth={ARC_STROKE} />
      <circle
        className="arc-fill arc-fill--baseline"
        cx={ARC_CENTER}
        cy={ARC_CENTER}
        r={ARC_R_INNER}
        strokeWidth={ARC_STROKE_INNER}
        strokeDasharray={inner.circumference}
        strokeDashoffset={inner.offset}
        transform={`rotate(-90 ${ARC_CENTER} ${ARC_CENTER})`}
      />
      <circle
        className="arc-fill arc-fill--actual"
        cx={ARC_CENTER}
        cy={ARC_CENTER}
        r={ARC_R}
        strokeWidth={ARC_STROKE}
        strokeDasharray={outer.circumference}
        strokeDashoffset={outer.offset}
        transform={`rotate(-90 ${ARC_CENTER} ${ARC_CENTER})`}
      />
      <text x={ARC_CENTER} y={ARC_CENTER - 3} textAnchor="middle" className="arc-num">
        {meanSamples.toFixed(2)}
      </text>
      <text x={ARC_CENTER} y={ARC_CENTER + 14} textAnchor="middle" className="arc-lbl">
        avg asked
      </text>
    </svg>
  )
}
