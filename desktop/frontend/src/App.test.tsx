import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, render, screen } from '@testing-library/react'
import App from './App'
import { GetHeads } from './bindings'

// Chat is replaced so this file tests only which of the two App shows.
vi.mock('./views/ChatView', () => ({ ChatView: () => <p>chat composer</p> }))

vi.mock('./bindings', () => ({
  GetHeads: vi.fn(),
  GetDashboard: vi.fn(() => new Promise(() => {})),
  GetFleet: vi.fn(() => new Promise(() => {})),
  GetPendingQuestions: vi.fn(() => Promise.resolve({ questions: [] })),
  GetVersion: vi.fn(() => new Promise(() => {})),
  GetUpdateStatus: vi.fn(() => new Promise(() => {})),
  GetSession: vi.fn(),
  GetEdits: vi.fn(),
  GetSecurity: vi.fn(),
  GetProviderKeys: vi.fn(() => Promise.resolve({ keys: [], store: 'x' })),
}))

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('what a first launch shows', () => {
  it('shows the connect-a-model steps, not a composer, while nothing can answer', async () => {
    vi.mocked(GetHeads).mockResolvedValue({ routable: 0, heads: [] })
    render(<App />)
    expect(await screen.findByText('Connect Hydra to an AI model')).toBeInTheDocument()
    expect(screen.queryByText('chat composer')).not.toBeInTheDocument()
    expect(screen.queryByText(/hyctl|PATH|terminal/)).not.toBeInTheDocument()
  })

  it('goes straight to chat on a machine that can already answer', async () => {
    vi.mocked(GetHeads).mockResolvedValue({
      routable: 1,
      heads: [{ id: 'env/openai', name: 'GPT-4o', provider: 'openai', source: 'env', tier: 4, capScore: 90, routable: true, localOnly: false }],
    })
    render(<App />)
    expect(await screen.findByText('chat composer')).toBeInTheDocument()
    expect(screen.queryByText('Connect Hydra to an AI model')).not.toBeInTheDocument()
  })

  // A head parked mid-session must not replace the conversation.
  it('does not take over Chat when a later probe finds nothing', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      vi.mocked(GetHeads).mockResolvedValueOnce({
        routable: 1,
        heads: [{ id: 'env/openai', name: 'GPT-4o', provider: 'openai', source: 'env', tier: 4, capScore: 90, routable: true, localOnly: false }],
      })
      vi.mocked(GetHeads).mockResolvedValue({ routable: 0, heads: [] })
      render(<App />)
      expect(await screen.findByText('chat composer')).toBeInTheDocument()
      await act(async () => {
        await vi.advanceTimersByTimeAsync(61_000)
      })
      expect(vi.mocked(GetHeads).mock.calls.length).toBeGreaterThan(1)
      expect(screen.getByText('chat composer')).toBeInTheDocument()
    } finally {
      vi.useRealTimers()
    }
  })
})
