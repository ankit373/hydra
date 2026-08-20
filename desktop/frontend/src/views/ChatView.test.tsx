import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { ChatView } from './ChatView'
import { Chat, GetDashboard, GetEdits, GetModels, GetSession, NewRunID } from '../bindings'
import type { ChatReply, Session as SessionData } from '../types'

// ChatView talks to the Go backend only through these bindings — mocking the
// module lets every test drive a specific backend outcome (a run still live,
// one that finished while the view was away, a plain dispatch failure) without
// a real Wails runtime.
vi.mock('../bindings', () => ({
  Chat: vi.fn(),
  GetDashboard: vi.fn(),
  GetEdits: vi.fn(),
  GetModels: vi.fn(),
  GetSession: vi.fn(),
  NewRunID: vi.fn(),
}))

const mockChat = vi.mocked(Chat)
const mockGetModels = vi.mocked(GetModels)
const mockGetDashboard = vi.mocked(GetDashboard)
const mockGetEdits = vi.mocked(GetEdits)
const mockGetSession = vi.mocked(GetSession)
const mockNewRunID = vi.mocked(NewRunID)

const TURNS_KEY = 'hydra.chat.turns'
const noop = () => {}

function emptySession(overrides: Partial<SessionData> = {}): SessionData {
  return {
    runId: '',
    live: false,
    found: false,
    timeline: [],
    agents: [],
    edges: [],
    nonLinear: false,
    skipped: 0,
    ...overrides,
  }
}

function renderDock(onOpenRun: (runID: string) => void = noop) {
  return render(<ChatView onOpenRun={onOpenRun} focusSignal={0} />)
}

beforeEach(() => {
  sessionStorage.clear()
  mockGetModels.mockResolvedValue({ found: true, pools: [] })
  mockGetEdits.mockResolvedValue([])
  // The companion pane reads the dashboard for the governor and calibration;
  // an unknown governor is the quiet default, so no notice fires in these tests.
  mockGetDashboard.mockResolvedValue({
    hasData: false,
    spend: { todayUsd: 0, allTimeUsd: 0, todayCalls: 0, totalCalls: 0, tokensActualPct: 0 },
    governor: {
      known: false,
      pct: 0,
      mode: '',
      effectiveMode: '',
      burnRatePct: 0,
      risk: 0,
      observations: 0,
      horizonObs: 3,
    },
    trust: {
      runs: 0,
      meanSamples: 0,
      fixedSwarmN: 0,
      samplesSavedPct: 0,
      autoClearedPct: 0,
      meanTargetConf: 0,
      meanFinalConf: 0,
      totalCostUsd: 0,
    },
    byModel: null,
    byTier: null,
    byDay: null,
    recent: null,
    calibration: [],
  })
  mockGetSession.mockResolvedValue(emptySession())
  let seq = 0
  mockNewRunID.mockImplementation(async () => `run-${++seq}`)
})

afterEach(() => {
  cleanup()
  vi.resetAllMocks()
})

describe('generic dispatch failures (#533)', () => {
  it('links a plain error turn to its session, like a successful one', async () => {
    const reply: ChatReply = {
      output: '',
      head: '',
      model: '',
      tier: 0,
      costUsd: 0,
      durationMs: 0,
      runId: 'run-1',
      error: 'dispatch: no route',
    }
    mockChat.mockResolvedValue(reply)
    const onOpenRun = vi.fn()
    renderDock(onOpenRun)

    const textarea = screen.getByPlaceholderText(/ask anything/i)
    fireEvent.change(textarea, { target: { value: 'do the thing' } })
    fireEvent.keyDown(textarea, { key: 'Enter' })

    expect(await screen.findByText('dispatch: no route')).toBeInTheDocument()
    const link = screen.getByRole('button', { name: /session →/ })

    fireEvent.click(link)
    expect(onOpenRun).toHaveBeenCalledWith('run-1')
  })

  it('leaves a needsProbe failure to its own retry button, with no session link', async () => {
    mockChat.mockResolvedValue({
      output: '',
      head: '',
      model: '',
      tier: 0,
      costUsd: 0,
      durationMs: 0,
      runId: 'run-1',
      error: 'No models found.',
      needsProbe: true,
    })
    renderDock()

    const textarea = screen.getByPlaceholderText(/ask anything/i)
    fireEvent.change(textarea, { target: { value: 'hi' } })
    fireEvent.keyDown(textarea, { key: 'Enter' })

    await screen.findByRole('button', { name: 'Check again' })
    expect(screen.queryByRole('button', { name: /session →/ })).not.toBeInTheDocument()
  })
})

describe('recovery after the view closes (#533)', () => {
  it('persists a completed turn across a remount', async () => {
    mockChat.mockResolvedValue({
      output: 'the answer',
      head: 'claude',
      model: 'Claude',
      tier: 2,
      costUsd: 0.01,
      durationMs: 500,
      runId: 'run-1',
    })
    const { unmount } = renderDock()

    const textarea = screen.getByPlaceholderText(/ask anything/i)
    fireEvent.change(textarea, { target: { value: 'remember me' } })
    fireEvent.keyDown(textarea, { key: 'Enter' })
    await screen.findByText('the answer')

    // A reload is nothing but a fresh mount sharing the same sessionStorage.
    unmount()
    renderDock()

    expect(await screen.findByText('remember me')).toBeInTheDocument()
    expect(screen.getByText('the answer')).toBeInTheDocument()
  })

  it('reattaches to a run left in-flight and resumes its live timeline', async () => {
    sessionStorage.setItem(TURNS_KEY, JSON.stringify([{ prompt: 'still going', runId: 'run-live' }]))
    mockGetSession.mockResolvedValue(
      emptySession({
        runId: 'run-live',
        found: true,
        live: true,
        timeline: [
          { kind: 'head_selected', ts: '', head: 'agy', tier: 4, costUsd: 0, durationMs: 0, confidence: 0 },
        ],
      }),
    )

    renderDock()

    expect(await screen.findByText('still going')).toBeInTheDocument()
    expect(await screen.findByText('working…')).toBeInTheDocument()
    expect(screen.getByText('head selected')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByPlaceholderText('working…')).toBeDisabled())
  })

  it('shows a recovery note rather than fabricated output once the run finished unseen', async () => {
    sessionStorage.setItem(TURNS_KEY, JSON.stringify([{ prompt: 'already done', runId: 'run-done' }]))
    mockGetSession.mockResolvedValue(
      emptySession({
        runId: 'run-done',
        found: true,
        live: false,
        agents: [
          {
            id: 'agy',
            depth: 0,
            tier: 3,
            state: 'ok',
            costUsd: 0.02,
            confidence: 0,
            durationMs: 900,
            head: 'agy',
            model: 'Antigravity',
          },
        ],
      }),
    )

    renderDock()

    expect(await screen.findByText(/finished while this view was closed/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /session →/ })).toBeInTheDocument()
    expect(screen.queryByText('the answer')).not.toBeInTheDocument()

    // Input unblocks once the recovered turn has settled.
    await waitFor(() => expect(screen.getByPlaceholderText(/ask anything/i)).not.toBeDisabled())
  })

  it('says so, rather than going silent, when a run cannot be found', async () => {
    sessionStorage.setItem(TURNS_KEY, JSON.stringify([{ prompt: 'mystery', runId: 'run-gone' }]))
    mockGetSession.mockResolvedValue(emptySession({ runId: 'run-gone', found: false, live: false }))

    renderDock()

    expect(await screen.findByText(/check fleet/i)).toBeInTheDocument()
  })
})
