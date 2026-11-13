import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { CalibrationLeaderboard, RecentTable, calBarWidth } from './Dashboard'
import type { CalibrationRow, RecentCall } from '../types'

afterEach(cleanup)

function row(d: number, n = 40, source = 'model:claude-sonnet'): CalibrationRow {
  return { source, domain: 'go', d, se: 0.6, sp: 0.6, n }
}

/** A bar's width in px on a track of the given width. */
function resolve(expr: string, trackPx: number): number {
  const m = /^([\d.]+)%$/.exec(expr)
  if (!m) throw new Error(`unparsed width: ${expr}`)
  return (Number(m[1]) / 100) * trackPx
}

describe('a calibration bar cannot exceed its track', () => {
  it('fills the track exactly at the top of the scale', () => {
    expect(resolve(calBarWidth(100), 200)).toBe(200)
  })

  it('clamps a width that overshoots rather than trusting its caller', () => {
    expect(resolve(calBarWidth(140), 200)).toBe(200)
    expect(resolve(calBarWidth(-5), 200)).toBe(0)
  })
})

describe('the leaderboard reads correctly on weak real data', () => {
  const weak = [row(0.28, 40, 'verifier:go-test'), row(0.28, 40), row(0.24, 40, 'model:qwen3')]

  it('draws three rows of D=0.28 as slivers, not three full bars', () => {
    const { container } = render(<CalibrationLeaderboard rows={weak} />)
    const fills = [...container.querySelectorAll<HTMLElement>('.cal__fill')]
    expect(fills).toHaveLength(3)
    for (const f of fills) {
      const w = resolve(f.style.width, 200)
      expect(w).toBeGreaterThan(0)
      expect(w).toBeLessThan(0.15 * 200)
    }
  })

  it('says in words that the evidence is weak', () => {
    render(<CalibrationLeaderboard rows={weak} />)
    expect(screen.getAllByText('weak evidence')).toHaveLength(3)
    expect(screen.queryByText('strong evidence')).toBeNull()
  })

  it('overflows nothing it renders', () => {
    const { container } = render(<CalibrationLeaderboard rows={[...weak, row(9, 400)]} />)
    for (const el of container.querySelectorAll<HTMLElement>('.cal__fill')) {
      expect(resolve(el.style.width, 200)).toBeLessThanOrEqual(200)
    }
  })

  it('marks a high D on few observations as provisional, not strong', () => {
    const { container } = render(<CalibrationLeaderboard rows={[row(2.4, 3)]} />)
    expect(screen.getByText('too few samples')).toBeTruthy()
    expect(container.querySelector('.cal__fill--thin')).toBeTruthy()
  })

  it('states the scale the bar is drawn against', () => {
    render(<CalibrationLeaderboard rows={weak} />)
    expect(screen.getByText(/full bar = one verdict enough for 95%/)).toBeTruthy()
  })

  it('renders the empty state rather than an empty track', () => {
    render(<CalibrationLeaderboard rows={[]} />)
    expect(screen.getByText('No checked answers yet')).toBeTruthy()
  })
})

describe('the Recent panel is a way in, not decoration', () => {
  const recent: RecentCall[] = [
    { ts: '2026-09-03T16:52:18Z', model: 'Gemini 3.5 Flash', tier: 8, costUsd: 0.0009, wallMs: 31300, runId: 'r-1', taskId: 't1' },
    // No run id: there is no run to open, so the row must stay inert.
    { ts: '2026-09-03T16:51:46Z', model: 'Antigravity', tier: 4, costUsd: 0, wallMs: 14300, runId: '', taskId: 't2' },
  ]

  it('opens the run behind a row', () => {
    const onOpenRun = vi.fn()
    render(<RecentTable rows={recent} onOpenRun={onOpenRun} />)
    fireEvent.click(screen.getByRole('button', { name: 'Open r-1' }))
    expect(onOpenRun).toHaveBeenCalledWith('r-1')
  })

  it('leaves a row with no run id inert rather than offering a dead control', () => {
    const onOpenRun = vi.fn()
    render(<RecentTable rows={recent} onOpenRun={onOpenRun} />)
    // Exactly one openable row, not two.
    expect(screen.getAllByRole('button', { name: /^Open / })).toHaveLength(1)
  })

  it('stays inert entirely when nothing can handle an open', () => {
    render(<RecentTable rows={recent} />)
    expect(screen.queryByRole('button', { name: /^Open / })).not.toBeInTheDocument()
  })
})
