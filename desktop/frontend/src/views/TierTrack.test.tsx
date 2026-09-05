import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import { TierTrack, tierSteps } from './TierTrack'
import type { TimelineEntry } from '../types'

afterEach(cleanup)

function e(kind: string, tier: number): TimelineEntry {
  return { kind, ts: '', tier, costUsd: 0, durationMs: 0, confidence: 0 }
}

describe('when a track would mislead, it does not render', () => {
  // A step line says "the router moved through these tiers in order". An SPRT
  // ensemble runs sources in parallel, so drawing it that way states something
  // false. Those get their own summary; this stays silent rather than lying.
  it('renders nothing for an SPRT ensemble', () => {
    expect(
      tierSteps([e('head_selected', 3), e('sample', 2), e('sample', 6)]),
    ).toBeNull()
  })

  it('renders nothing for a swarm fan-out', () => {
    expect(
      tierSteps([e('head_selected', 3), e('attempt', 2), e('attempt', 6)]),
    ).toBeNull()
  })

  // Same reasoning Session already uses to hide its Graph tab on a linear run:
  // a chart of a thing that did not move is noise.
  it('renders nothing when the tier never changed', () => {
    expect(tierSteps([e('head_selected', 6), e('dispatch_finished', 6)])).toBeNull()
  })

  it('renders nothing when no entry carries a tier', () => {
    expect(tierSteps([e('run_started', 0), e('edit', 0)])).toBeNull()
  })

  it('mounts to nothing rather than an empty box', () => {
    const { container } = render(<TierTrack entries={[e('head_selected', 6)]} />)
    expect(container).toBeEmptyDOMElement()
  })
})

describe('the sequence it extracts', () => {
  it('collapses consecutive entries at the same tier into one step', () => {
    const steps = tierSteps([
      e('head_selected', 6),
      e('dispatch_started', 6),
      e('error', 6),
      e('head_selected', 3),
      e('dispatch_finished', 3),
    ])
    expect(steps).toEqual([
      { tier: 6, held: 3 },
      { tier: 3, held: 2 },
    ])
  })

  it('keeps a return to an earlier tier as its own step, not a merge', () => {
    const steps = tierSteps([e('head_selected', 6), e('head_selected', 3), e('head_selected', 6)])
    expect(steps?.map((s) => s.tier)).toEqual([6, 3, 6])
  })

  it('ignores entries with no tier without breaking the run of one', () => {
    const steps = tierSteps([e('head_selected', 6), e('edit', 0), e('dispatch_finished', 6)])
    // The edit carries no tier, so tier 6 was never actually left.
    expect(steps).toBeNull()
  })
})

describe('what it says out loud', () => {
  // Lower tier number is stronger, so 6 → 2 is an escalation, not a decrease.
  it('calls a move to a stronger tier an escalation', () => {
    render(<TierTrack entries={[e('head_selected', 6), e('head_selected', 2)]} />)
    expect(screen.getByText(/escalated once · T6 → T2/)).toBeInTheDocument()
  })

  it('calls a move to a cheaper tier easing off', () => {
    render(<TierTrack entries={[e('head_selected', 2), e('head_selected', 8)]} />)
    expect(screen.getByText(/eased off once · T2 → T8/)).toBeInTheDocument()
  })

  it('does not claim a direction when it ended where it started', () => {
    render(<TierTrack entries={[e('head_selected', 6), e('head_selected', 3), e('head_selected', 6)]} />)
    expect(screen.getByText(/ended where it started/)).toBeInTheDocument()
  })

  it('counts multiple hops', () => {
    render(
      <TierTrack entries={[e('head_selected', 8), e('head_selected', 6), e('head_selected', 2)]} />,
    )
    expect(screen.getByText(/escalated 2 times/)).toBeInTheDocument()
  })

  it('gives the shape an accessible reading, not just a picture', () => {
    render(<TierTrack entries={[e('head_selected', 6), e('head_selected', 2)]} />)
    expect(screen.getByRole('img', { name: 'Tier movement: T6 then T2' })).toBeInTheDocument()
  })
})
