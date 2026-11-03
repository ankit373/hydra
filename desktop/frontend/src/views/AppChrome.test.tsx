import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import { AppHeader } from './AppHeader'
import { AppFooter } from './AppFooter'

vi.mock('../bindings', () => ({
  GetUpdateStatus: vi.fn().mockResolvedValue({ current: 'v1.5.0', available: false }),
  TriggerUpgrade: vi.fn(),
}))

afterEach(cleanup)

const header = (over: { routable?: number | null; headsError?: string | null } = {}) =>
  render(
    <AppHeader
      nav={[{ id: 'chat', label: 'Chat' }]}
      current="chat"
      onSelect={() => {}}
      onSearch={() => {}}
      routable={over.routable ?? null}
      headsError={over.headsError ?? null}
      todayUsd={null}
      onHeads={() => {}}
      onSpend={() => {}}
    />,
  )

const footer = (over: { routable?: number | null; headsError?: string | null } = {}) =>
  render(
    <AppFooter
      version={null}
      routable={over.routable ?? null}
      headsError={over.headsError ?? null}
      total={over.routable == null ? null : 3}
      local={null}
      todayUsd={null}
      calls={null}
      mode=""
    />,
  )

// A probe in flight and a probe that has failed were both null, so a dead
// backend read as "still working on it", for ever. Measured in a real browser:
// the header said "probing…" and the footer "probing heads…" indefinitely
// against a backend that refused every call (#1100).
describe('the head count distinguishes three states, not two', () => {
  it('says it is probing while nothing has answered yet', () => {
    header()
    expect(screen.getByText('probing…')).toBeInTheDocument()
  })

  it('says the probe failed once it has', () => {
    header({ headsError: 'dial tcp 127.0.0.1:11434: connect: connection refused' })
    expect(screen.getByText('probe failed')).toBeInTheDocument()
    expect(screen.queryByText('probing…')).not.toBeInTheDocument()
  })

  it('keeps the last good count rather than reverting to either', () => {
    // The polls deliberately keep the previous value; a failed tick must not
    // report zero heads on a machine that has some.
    header({ routable: 2, headsError: 'connection refused' })
    expect(screen.getByText('2 routable')).toBeInTheDocument()
    expect(screen.queryByText('probe failed')).not.toBeInTheDocument()
  })

  it('carries the reason on the control rather than only the state', () => {
    header({ headsError: 'connection refused' })
    expect(screen.getByTitle('connection refused')).toBeInTheDocument()
  })

  it('makes the same distinction in the footer', () => {
    const { unmount } = footer()
    expect(screen.getByText('probing heads…')).toBeInTheDocument()
    unmount()

    footer({ headsError: 'connection refused' })
    expect(screen.getByText('probe failed')).toBeInTheDocument()
    expect(screen.queryByText('probing heads…')).not.toBeInTheDocument()
  })
})
