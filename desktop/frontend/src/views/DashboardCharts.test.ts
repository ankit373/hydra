import { describe, expect, it } from 'vitest'
import { layout } from './DashboardCharts'
import type { Breakdown } from '../types'

function days(n: number, cost = 1): Breakdown[] {
  return Array.from({ length: n }, (_, i) => ({
    key: `2026-09-${String(i + 1).padStart(2, '0')}`,
    calls: 10,
    promptTokens: 0,
    responseTokens: 0,
    costUsd: cost,
    wallMs: 0,
  }))
}

/** Right edge of the last bar: what "fills its container" actually means. */
function right(bars: ReturnType<typeof layout>): number {
  const last = bars[bars.length - 1]
  return last.x + last.w
}

describe('the daily-spend chart fills the width it is given', () => {
  // The defect: a fixed 16px bar at an 8px pitch put fourteen days in ~328px,
  // so they occupied about a third of a 1900px container and the rest was
  // empty black. Nothing failed; it just looked unfinished (#1098).
  it('spans the container rather than a fixed pitch', () => {
    const bars = layout(days(14), 1900)
    expect(bars).toHaveLength(14)
    expect(right(bars)).toBeGreaterThan(1900 * 0.9)
    expect(right(bars)).toBeLessThanOrEqual(1900)
  })

  it('still spans it at a narrow width', () => {
    const bars = layout(days(14), 420)
    expect(right(bars)).toBeGreaterThan(420 * 0.9)
    expect(right(bars)).toBeLessThanOrEqual(420)
  })

  // Filling by stretching is the wrong fix: two days would render as slabs.
  it('caps the bar width instead of stretching a short series', () => {
    const bars = layout(days(2), 1900)
    for (const b of bars) expect(b.w).toBeLessThanOrEqual(44)
  })

  // And the other end: a year of days must stay visible, not vanish.
  it('keeps a bar wide enough to see when there are many', () => {
    const bars = layout(days(365), 900)
    for (const b of bars) expect(b.w).toBeGreaterThanOrEqual(6)
  })

  it('never overlaps two bars', () => {
    for (const n of [2, 7, 14, 60]) {
      const bars = layout(days(n), 1200)
      for (let i = 1; i < bars.length; i++) {
        expect(bars[i].x).toBeGreaterThanOrEqual(bars[i - 1].x + bars[i - 1].w)
      }
    }
  })

  it('gives every bar the same width, so height is the only variable', () => {
    const bars = layout(
      [...days(3, 5), ...days(3, 0.01)].map((d, i) => ({ ...d, key: `d${i}` })),
      800,
    )
    expect(new Set(bars.map((b) => b.w)).size).toBe(1)
  })

  // A zero-width container happens during an unmount or on a hidden tab, and
  // dividing by the day count there would place every bar at NaN.
  it('returns nothing rather than NaN positions at zero width', () => {
    expect(layout(days(14), 0)).toEqual([])
    expect(layout([], 900)).toEqual([])
  })

  it('still gives a free day a visible nub rather than dropping it', () => {
    const bars = layout(days(3, 0), 900)
    for (const b of bars) expect(b.barH).toBeGreaterThan(0)
  })
})
