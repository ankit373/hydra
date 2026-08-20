import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { GovernorNotice } from './GovernorNotice'
import type { GovernorPanel } from '../types'

afterEach(cleanup)

function gov(over: Partial<GovernorPanel> = {}): GovernorPanel {
  return {
    known: true,
    pct: 76,
    mode: 'critical',
    effectiveMode: 'critical',
    burnRatePct: 2.1,
    risk: 0.62,
    observations: 11,
    horizonObs: 3,
    ...over,
  }
}

describe('when it speaks up', () => {
  it('stays quiet below the critical band', () => {
    render(<GovernorNotice governor={gov({ pct: 55, mode: 'compact', effectiveMode: 'compact' })} busy={false} />)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('stays quiet while a dispatch is in flight, since it cannot be acted on mid-answer', () => {
    render(<GovernorNotice governor={gov()} busy={true} />)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('stays quiet when nothing has measured the orchestrator at all', () => {
    render(<GovernorNotice governor={gov({ known: false })} busy={false} />)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('speaks up at critical, and once dismissed stays dismissed for that band', () => {
    render(<GovernorNotice governor={gov()} busy={false} />)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /got it/i }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })
})

describe('what it claims', () => {
  it('projects headroom from the burn rate', () => {
    // 76% now, 2.1pp per update → ceil(4/2.1) = 2 updates of headroom.
    render(<GovernorNotice governor={gov()} busy={false} />)
    expect(screen.getByText(/2 updates/)).toBeInTheDocument()
    expect(screen.getByText(/62%/)).toBeInTheDocument()
  })

  // RiskFromHistory returns zero below two observations. Presenting that as a
  // measured "no risk" is the exact trap the observations count exists to avoid.
  it('does not invent a projection when there is no rate signal', () => {
    render(
      <GovernorNotice
        governor={gov({ observations: 1, burnRatePct: 0, risk: 0 })}
        busy={false}
      />,
    )
    expect(screen.getByText(/not enough history/i)).toBeInTheDocument()
    expect(screen.queryByText(/per update/)).not.toBeInTheDocument()
  })

  it('says when the rate, not the level, is what raised the band', () => {
    render(
      <GovernorNotice governor={gov({ pct: 64, mode: 'compact', effectiveMode: 'critical' })} busy={false} />,
    )
    expect(screen.getByText(/how fast this is climbing/i)).toBeInTheDocument()
  })

  // A real probability must never render as a flat 0%.
  it('floors a tiny probability at <1% rather than 0%', () => {
    render(<GovernorNotice governor={gov({ risk: 0.002 })} busy={false} />)
    expect(screen.getByText(/<1%/)).toBeInTheDocument()
  })
})
