import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { Rail, type NavItem } from './Rail'

vi.mock('../bindings', () => ({
  GetUpdateStatus: vi.fn().mockResolvedValue({ current: 'v1.5.0', available: false }),
  TriggerUpgrade: vi.fn(),
}))

afterEach(cleanup)

const NAV: NavItem[] = [
  { id: 'chat', label: 'Chat' },
  { id: 'activity', label: 'Activity', note: '1 waiting', urgent: true },
]

const rail = (
  over: { routable?: number | null; headsError?: string | null; onSelect?: (id: string) => void } = {},
) =>
  render(
    <Rail
      nav={NAV}
      current="chat"
      onSelect={over.onSelect ?? (() => {})}
      onSearch={() => {}}
      routable={over.routable ?? null}
      total={over.routable == null ? null : 3}
      headsError={over.headsError ?? null}
      todayUsd={null}
      calls={null}
      mode=""
      version={null}
      onHeads={() => {}}
      onSpend={() => {}}
    />,
  )

// A probe in flight and a probe that has failed were both null, so a dead
// backend read as "still working on it", for ever (#1100).
describe('the model count distinguishes three states, not two', () => {
  it('says it is looking while nothing has answered yet', () => {
    rail()
    expect(screen.getByText('looking for models…')).toBeInTheDocument()
  })

  it('says the probe failed once it has', () => {
    rail({ headsError: 'dial tcp 127.0.0.1:11434: connect: connection refused' })
    expect(screen.getByText('probe failed')).toBeInTheDocument()
    expect(screen.queryByText('looking for models…')).not.toBeInTheDocument()
  })

  it('keeps the last good count rather than reverting to either', () => {
    rail({ routable: 2, headsError: 'connection refused' })
    expect(screen.getByRole('button', { name: '2 of 3 models ready' })).toBeInTheDocument()
    expect(screen.queryByText('probe failed')).not.toBeInTheDocument()
  })

  it('carries the reason on the control rather than only the state', () => {
    rail({ headsError: 'connection refused' })
    expect(screen.getByTitle('connection refused')).toBeInTheDocument()
  })
})

describe('the rail', () => {
  it('marks the open view and switches on click', () => {
    const onSelect = vi.fn()
    rail({ onSelect })
    expect(screen.getByRole('button', { name: 'Chat' })).toHaveAttribute('aria-current', 'page')
    fireEvent.click(screen.getByRole('button', { name: /Activity/ }))
    expect(onSelect).toHaveBeenCalledWith('activity')
  })

  it('says in words what is waiting, not only a number', () => {
    rail()
    expect(screen.getByText('1 waiting')).toHaveClass('rail__note--urgent')
  })
})
