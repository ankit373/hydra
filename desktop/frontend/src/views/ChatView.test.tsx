import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { ChatView } from './ChatView'
import {
  AnswerQuestion,
  Chat,
  DeclineQuestion,
  GetDashboard,
  GetEdits,
  GetModels,
  GetSession,
  NewRunID,
  onChatStream,
} from '../bindings'
import type { ChatReply, Session as SessionData } from '../types'
import type { ChatStreamEvent } from '../chatStream'

// ChatView talks to the Go backend only through these bindings, mocking the
// module lets every test drive a specific backend outcome (a run still live,
// one that finished while the view was away, a plain dispatch failure) without
// a real Wails runtime.
vi.mock('../bindings', () => ({
  AnswerQuestion: vi.fn(),
  Chat: vi.fn(),
  DeclineQuestion: vi.fn(),
  GetDashboard: vi.fn(),
  GetEdits: vi.fn(),
  GetModels: vi.fn(),
  GetSession: vi.fn(),
  NewRunID: vi.fn(),
  onChatStream: vi.fn(),
}))

const mockChat = vi.mocked(Chat)
const mockAnswer = vi.mocked(AnswerQuestion)
const mockDecline = vi.mocked(DeclineQuestion)
const mockGetModels = vi.mocked(GetModels)
const mockGetDashboard = vi.mocked(GetDashboard)
const mockGetEdits = vi.mocked(GetEdits)
const mockGetSession = vi.mocked(GetSession)
const mockNewRunID = vi.mocked(NewRunID)
const mockOnChatStream = vi.mocked(onChatStream)

// The view subscribes once on mount, so a test drives the stream by keeping the
// callback it registered and calling it, which is what Wails does.
let pushEvent: (ev: ChatStreamEvent) => void = () => {}

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
  mockOnChatStream.mockImplementation((cb) => {
    pushEvent = cb
    return () => {
      pushEvent = () => {}
    }
  })
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

// A ledger policy can answer `ask`, which parks the task before anything runs
// (#582). The transcript is where that has to surface: a modal that vanishes
// leaves the task silently parked.
describe('a task parked waiting on a human (#583)', () => {
  function parked(over: Partial<ChatReply> = {}): ChatReply {
    return {
      output: '',
      head: 'gated',
      model: '',
      tier: 0,
      costUsd: 0,
      durationMs: 0,
      runId: 'run-1',
      question: 'Allow gated to run this task? It would act on internal/auth/token.go.',
      taskId: 'task-1',
      ...over,
    }
  }

  async function askAndPark(over: Partial<ChatReply> = {}) {
    mockChat.mockResolvedValue(parked(over))
    renderDock()
    const textarea = screen.getByPlaceholderText(/ask anything/i)
    fireEvent.change(textarea, { target: { value: 'rotate the signing key' } })
    fireEvent.keyDown(textarea, { key: 'Enter' })
    return screen.findByText(/Allow gated to run this task/)
  }

  it('shows the question in the transcript rather than as an error', async () => {
    await askAndPark()
    expect(screen.getByText(/waiting on you/i)).toBeInTheDocument()
    // The distinction that matters: this needs you, it did not break.
    expect(screen.queryByText(/dispatch/i)).not.toBeInTheDocument()
  })

  it('answers the task and replaces the question with the result', async () => {
    await askAndPark()
    mockAnswer.mockResolvedValue({
      output: 'rotated', head: 'gated', model: 'Gated', tier: 3,
      costUsd: 0.01, durationMs: 12, runId: 'run-1',
    })

    fireEvent.change(screen.getByLabelText(/your answer/i), { target: { value: 'yes, go ahead' } })
    fireEvent.click(screen.getByRole('button', { name: 'Answer' }))

    await waitFor(() => expect(mockAnswer).toHaveBeenCalledWith('task-1', 'yes, go ahead'))
    expect(await screen.findByText('rotated')).toBeInTheDocument()
    expect(screen.queryByText(/waiting on you/i)).not.toBeInTheDocument()
  })

  // No default action, nothing that resolves on dismiss: an unanswered
  // question is not an approval.
  it('will not answer with an empty box', async () => {
    await askAndPark()
    const go = screen.getByRole('button', { name: 'Answer' })
    expect(go).toBeDisabled()

    fireEvent.change(screen.getByLabelText(/your answer/i), { target: { value: '   ' } })
    expect(go).toBeDisabled()
    fireEvent.click(go)
    expect(mockAnswer).not.toHaveBeenCalled()
  })

  it('declines without running anything', async () => {
    await askAndPark()
    mockDecline.mockResolvedValue(undefined)

    fireEvent.click(screen.getByRole('button', { name: 'Decline' }))

    await waitFor(() => expect(mockDecline).toHaveBeenCalledWith('task-1', ''))
    expect(await screen.findByText(/declined\. nothing ran\./i)).toBeInTheDocument()
    expect(mockAnswer).not.toHaveBeenCalled()
  })

  // Approval is per head, so a resumed dispatch can land on a head the human
  // was never shown and park again. That has to read as a new question.
  it('shows a second question when answering parks again', async () => {
    await askAndPark()
    mockAnswer.mockResolvedValue(parked({ question: 'Allow other to run this task?', taskId: 'task-2' }))

    fireEvent.change(screen.getByLabelText(/your answer/i), { target: { value: 'yes' } })
    fireEvent.click(screen.getByRole('button', { name: 'Answer' }))

    expect(await screen.findByText('Allow other to run this task?')).toBeInTheDocument()
    expect(screen.getByText(/waiting on you/i)).toBeInTheDocument()
  })

  it('answers on Enter as well as the button', async () => {
    await askAndPark()
    mockAnswer.mockResolvedValue({
      output: 'done', head: 'gated', model: 'Gated', tier: 3,
      costUsd: 0, durationMs: 1, runId: 'run-1',
    })

    const box = screen.getByLabelText(/your answer/i)
    fireEvent.change(box, { target: { value: 'go' } })
    fireEvent.keyDown(box, { key: 'Enter' })

    await waitFor(() => expect(mockAnswer).toHaveBeenCalledWith('task-1', 'go'))
  })
  it('names the model that could not answer, and the one that did', async () => {
    // #676: a T1 pick answered by a T10 local head, with nothing said. The
    // reply used to carry only the winner, so this read as ordinary routing.
    mockChat.mockResolvedValue({
      output: 'sure',
      head: 'ollama/qwen', model: 'Qwen2.5-Coder:7b (Ollama)', tier: 10,
      costUsd: 0, durationMs: 1700, runId: 'run-1',
      attempts: [{ head: 'claude', model: 'Claude Code', tier: 1, reason: 'exit status 1' }],
    })
    renderDock(vi.fn())

    const textarea = screen.getByPlaceholderText(/ask anything/i)
    fireEvent.change(textarea, { target: { value: 'is claude code working' } })
    fireEvent.keyDown(textarea, { key: 'Enter' })

    expect(await screen.findByText(/could not answer/i)).toHaveTextContent('Claude Code')
    expect(screen.getByText(/could not answer/i)).toHaveTextContent('exit status 1')
    expect(screen.getByText(/Answered by/i)).toHaveTextContent('Qwen2.5-Coder:7b (Ollama)')
  })

  it('says nothing about fallbacks when the first model answered', async () => {
    mockChat.mockResolvedValue({
      output: 'sure', head: 'claude', model: 'Claude Code', tier: 1,
      costUsd: 0, durationMs: 900, runId: 'run-1',
    })
    renderDock(vi.fn())

    const textarea = screen.getByPlaceholderText(/ask anything/i)
    fireEvent.change(textarea, { target: { value: 'hello' } })
    fireEvent.keyDown(textarea, { key: 'Enter' })

    await screen.findByRole('button', { name: /session →/ })
    expect(screen.queryByText(/could not answer/i)).not.toBeInTheDocument()
  })
})

// The dispatch as it happens, rather than a window that shows nothing until the
// whole answer exists (#799). Driven through the callback the view registered,
// which is what Wails calls.
describe('a streaming dispatch', () => {
  const streamEvent = (over: Partial<ChatStreamEvent>): ChatStreamEvent => ({
    runID: 'run-1',
    kind: 'delta',
    head: 'ollama/qwen3',
    tier: 10,
    offset: 0,
    recoverable: false,
    ...over,
  })

  // Held open so the turn stays in flight while events arrive, which is the
  // whole window this feature exists for.
  function sendAndHold() {
    let finish: (r: ChatReply) => void = () => {}
    mockNewRunID.mockResolvedValue('run-1')
    mockChat.mockReturnValue(
      new Promise<ChatReply>((res) => {
        finish = res
      }),
    )
    renderDock()
    const textarea = screen.getByPlaceholderText(/ask anything/i)
    fireEvent.change(textarea, { target: { value: 'write me a haiku' } })
    fireEvent.keyDown(textarea, { key: 'Enter' })
    return { finish }
  }

  it('grows the message on screen as deltas arrive', async () => {
    sendAndHold()
    await waitFor(() => expect(mockChat).toHaveBeenCalled())

    act(() => {
      pushEvent(streamEvent({ kind: 'started' }))
      pushEvent(streamEvent({ text: 'an old ', offset: 0 }))
    })
    expect(await screen.findByText(/an old/)).toBeInTheDocument()

    act(() => {
      pushEvent(streamEvent({ text: 'silent pond', offset: 7 }))
    })
    expect(await screen.findByText('an old silent pond')).toBeInTheDocument()
    // The placeholder is gone once there is an answer to look at; both at once
    // reads as a stall.
    expect(screen.queryByText(/routing…|working…/)).not.toBeInTheDocument()
  })

  it('replaces the streamed text with the finished reply, not duplicating it', async () => {
    const { finish } = sendAndHold()
    await waitFor(() => expect(mockChat).toHaveBeenCalled())
    act(() => {
      pushEvent(streamEvent({ kind: 'started' }))
      pushEvent(streamEvent({ text: 'an old silent pond', offset: 0 }))
    })
    await screen.findByText('an old silent pond')

    act(() => {
      finish({
        output: 'an old silent pond',
        head: 'ollama/qwen3',
        model: 'qwen3',
        tier: 10,
        costUsd: 0,
        durationMs: 120,
        runId: 'run-1',
      })
    })

    // Exactly one copy: the live block is dropped when the turn completes, and
    // the finished turn renders from reply.output.
    await waitFor(() => {
      expect(screen.getAllByText('an old silent pond')).toHaveLength(1)
    })
  })

  it('collapses an abandoned attempt to one line, expandable to its partial', async () => {
    sendAndHold()
    await waitFor(() => expect(mockChat).toHaveBeenCalled())

    act(() => {
      // recoverable is a run-wide setting (payload capture), so the backend
      // puts it on every event, not only the one that reads it.
      pushEvent(
        streamEvent({ kind: 'started', head: 'openrouter/gpt', tier: 4, recoverable: true }),
      )
      pushEvent(
        streamEvent({
          text: 'half an answ',
          offset: 0,
          head: 'openrouter/gpt',
          recoverable: true,
        }),
      )
      pushEvent(
        streamEvent({
          kind: 'failed',
          head: 'openrouter/gpt',
          reason: '429 rate limited',
          spanID: 'span-a',
          offset: 12,
          recoverable: true,
        }),
      )
      pushEvent(streamEvent({ kind: 'started', head: 'claude', tier: 1 }))
      pushEvent(streamEvent({ text: 'the real answer', offset: 0, head: 'claude' }))
    })

    expect(await screen.findByText(/abandoned/)).toBeInTheDocument()
    expect(screen.getByText(/429 rate limited/)).toBeInTheDocument()
    // Present but collapsed, not deleted: it is evidence, and the point is that
    // it does not stand in the transcript as if it were the answer.
    expect(screen.getByText('half an answ')).not.toBeVisible()
    expect(screen.getByText('the real answer')).toBeVisible()

    fireEvent.click(screen.getByText(/abandoned/))
    expect(screen.getByText('half an answ')).toBeVisible()
    // Payload capture was on, so the span really can be read back.
    expect(screen.getByText(/hyctl trace view run-1 --span span-a/)).toBeInTheDocument()
  })

  // A missed event means what is on screen is not the whole answer. Saying so
  // beats rendering a spliced one that looks complete.
  it('says so when an event went missing rather than splicing over it', async () => {
    sendAndHold()
    await waitFor(() => expect(mockChat).toHaveBeenCalled())

    act(() => {
      pushEvent(streamEvent({ kind: 'started' }))
      pushEvent(streamEvent({ text: 'an old ', offset: 0 }))
      pushEvent(streamEvent({ text: 'pond', offset: 999 }))
    })

    expect(await screen.findByText(/did not reach the window/i)).toBeInTheDocument()
    expect(screen.queryByText('an old pond')).not.toBeInTheDocument()
  })

  // Events from a dispatch that has already finished must not attach themselves
  // to whatever is on screen now.
  it('ignores an event from another run', async () => {
    sendAndHold()
    await waitFor(() => expect(mockChat).toHaveBeenCalled())

    act(() => {
      pushEvent(streamEvent({ kind: 'started' }))
      pushEvent(streamEvent({ text: 'mine', offset: 0 }))
      pushEvent(streamEvent({ runID: 'run-2', text: ' theirs', offset: 4 }))
    })

    expect(await screen.findByText('mine')).toBeInTheDocument()
    expect(screen.queryByText('mine theirs')).not.toBeInTheDocument()
  })
})
