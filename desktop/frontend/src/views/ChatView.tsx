import { useEffect, useRef, useState } from 'react'
import { AnswerQuestion, Chat, DeclineQuestion, GetDashboard, GetSession, NewRunID } from '../bindings'
import type { ChatReply, GovernorPanel, Session as SessionData } from '../types'
import { ms, usdExact } from '../format'
import { Timeline } from './Session'
import { ModelPicker } from './ModelPicker'
import { Cockpit } from './Cockpit'
import { GovernorNotice } from './GovernorNotice'

// Matches App.tsx's LIVE_MS, same reasoning: this is "what is happening now",
// not a retrospective read.
const POLL_MS = 2000

// Turn history survives a reload via sessionStorage, cleared when the window
// closes, which is the lifetime a still-open chat already implies. Own key so
// a future view's persistence cannot collide with it.
const TURNS_KEY = 'hydra.chat.turns'

interface Turn {
  prompt: string
  runId: string
  reply?: ChatReply
  // Set instead of trusting `reply` at face value when this turn's outcome
  // was reconstructed from the run log after a reload rather than returned by
  // Chat() itself. runlog never inlines model output (its own doc says so),
  // so a recovered turn can carry routing/cost but never the answer text,
  // this is what says so, rather than rendering a blank reply as if real.
  recoveredNote?: string
}

/**
 * The chat view, Hydra's default surface (#520).
 *
 * Was a collapsible dock, whose whole rationale was avoiding split attention
 * between a chat and the view it commented on. As the primary surface there is
 * no second view to split from, so the dock's collapse behaviour is gone and
 * the reasoning is answered instead by keeping the companion sidebar
 * glanceable rather than readable.
 *
 * Turn history persists in sessionStorage, so navigating away and back does not
 * lose the conversation, and a dispatch cut off mid-flight is reattached from
 * its run log rather than treated as lost (#533).
 *
 * focusSignal is a counter App bumps when something elsewhere (Fleet's empty
 * state, #422) asks this view to take the caret, so it works even when the
 * view is already open.
 */
export function ChatView({
  onOpenRun,
  focusSignal,
}: {
  onOpenRun: (runID: string) => void
  focusSignal: number
}) {
  const [prompt, setPrompt] = useState('')
  // Empty means auto-route. The model's own id, not its tier: a tier cannot
  // say which of two T1 heads was picked, and the router was free to answer
  // from a different one (#676).
  const [head, setHead] = useState('')
  const [turns, setTurns] = useState<Turn[]>([])
  const [busy, setBusy] = useState(false)
  // The one turn currently in flight, if any, Chat's own busy flag already
  // guarantees at most one, so this doesn't need to be keyed by turn index.
  const [liveSession, setLiveSession] = useState<SessionData | null>(null)
  const [governor, setGovernor] = useState<GovernorPanel | undefined>()
  const logRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLTextAreaElement>(null)
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null)

  // A dispatch keeps running even if the window closes mid-poll; the interval
  // itself must not outlive the component.
  useEffect(() => () => stopPolling(), [])

  function stopPolling() {
    if (pollRef.current !== null) {
      clearInterval(pollRef.current)
      pollRef.current = null
    }
  }

  // Runlog is written incrementally from the moment Chat begins (chat.go
  // appends KindRunStarted before dispatching), so GetSession has something to
  // show well before the blocking Chat call returns, this is what makes a
  // chat turn narrate the way Session's own Timeline already does, instead of
  // sitting behind a spinner until the whole thing finishes (#513).
  function watchRun(runId: string) {
    setLiveSession(null)
    const poll = () => void GetSession(runId).then(setLiveSession).catch(() => {})
    poll()
    pollRef.current = setInterval(poll, POLL_MS)
  }

  // Reattaches to a run this page did not start watching, either a reload
  // caught it mid-dispatch, or the log needs one read to learn it already
  // finished. chat.go's dispatch runs on its own context, unaffected by the
  // frontend reloading (#533), so the run itself is never actually lost,
  // only this page's earlier view of it was. Keeps polling like a fresh
  // dispatch until the run stops being live, then settles the turn from
  // whatever the log recorded, since the real Chat() promise that would have
  // carried the answer text back died with the page that awaited it.
  function reattach(idx: number, runId: string) {
    setBusy(true)
    const poll = () => {
      void GetSession(runId)
        .then((s) => {
          if (s.live) {
            setLiveSession(s)
            return
          }
          stopPolling()
          setLiveSession(null)
          setBusy(false)
          const note = s.found
            ? 'Finished while this view was closed, the run record was kept, not the answer text.'
            : "Couldn't confirm this task's status, check Fleet."
          setTurns((t) =>
            t.map((turn, i) =>
              i === idx ? { ...turn, reply: recoveredReply(runId, s), recoveredNote: note } : turn,
            ),
          )
        })
        .catch(() => {})
    }
    poll()
    pollRef.current = setInterval(poll, POLL_MS)
  }

  // Once, on mount: load whatever turn history survived a reload, and if the
  // last one never got a reply, it was cut off mid-dispatch rather than
  // actually lost (#533).
  useEffect(() => {
    let saved: Turn[]
    try {
      saved = JSON.parse(sessionStorage.getItem(TURNS_KEY) ?? '[]')
    } catch {
      saved = []
    }
    if (!Array.isArray(saved) || saved.length === 0) return
    setTurns(saved)
    const last = saved[saved.length - 1]
    if (last && !last.reply) reattach(saved.length - 1, last.runId)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // The inverse of the effect above: every change is what a later reload
  // would load.
  useEffect(() => {
    try {
      sessionStorage.setItem(TURNS_KEY, JSON.stringify(turns))
    } catch {
      /* Storage can be full or disabled; losing persistence must not break chat. */
    }
  }, [turns])

  useEffect(() => {
    logRef.current?.scrollTo({ top: logRef.current.scrollHeight })
  }, [turns])

  // Orchestrator pressure is a slow, session-wide value, so it ticks on its own
  // rather than per turn.
  useEffect(() => {
    const tick = () => void GetDashboard().then((d) => setGovernor(d.governor)).catch(() => {})
    tick()
    const t = setInterval(tick, 5000)
    return () => clearInterval(t)
  }, [])

  async function send() {
    const p = prompt.trim()
    if (!p || busy) return
    setPrompt('')
    setBusy(true)
    // Minted before dispatching (not read off the reply) so watchRun can
    // start polling immediately, Chat won't resolve until the whole
    // dispatch finishes, but the run's log exists from the moment it starts.
    const runId = await NewRunID()
    setTurns((t) => [...t, { prompt: p, runId }])
    watchRun(runId)
    try {
      const reply = await Chat(p, '', runId, '', head)
      setTurns((t) => t.map((turn, i) => (i === t.length - 1 ? { ...turn, reply } : turn)))
    } catch (e) {
      const message = e instanceof Error ? e.message : String(e)
      setTurns((t) =>
        t.map((turn, i) =>
          i === t.length - 1
            ? { ...turn, reply: { ...emptyReply(), error: message } }
            : turn,
        ),
      )
    } finally {
      stopPolling()
      setLiveSession(null)
      setBusy(false)
    }
  }

  // No heads discoverable at all, dispatch.New already probes fresh on every
  // call, so retrying the same prompt IS the "look again" action, not a
  // separate one. Targets a turn by index and reuses its own stored prompt
  // rather than the (already-cleared) input state. A fresh run id, same as
  // any other dispatch attempt, the failed attempt's log stays exactly what
  // it was, a probe that found nothing.
  async function retry(i: number) {
    if (busy) return
    const p = turns[i].prompt
    setBusy(true)
    const runId = await NewRunID()
    setTurns((t) => t.map((turn, idx) => (idx === i ? { ...turn, runId } : turn)))
    watchRun(runId)
    try {
      const reply = await Chat(p, '', runId, '', head)
      setTurns((t) => t.map((turn, idx) => (idx === i ? { ...turn, reply } : turn)))
    } catch (e) {
      const message = e instanceof Error ? e.message : String(e)
      setTurns((t) =>
        t.map((turn, idx) => (idx === i ? { ...turn, reply: { ...emptyReply(), error: message } } : turn)),
      )
    } finally {
      stopPolling()
      setLiveSession(null)
      setBusy(false)
    }
  }

  // Answering a parked task is a dispatch like any other: same busy flag, same
  // live timeline, and the answer replaces the question in place so the
  // transcript reads as one exchange rather than two unrelated entries.
  async function answer(i: number, text: string) {
    const t = turns[i]
    const taskId = t.reply?.taskId
    if (!taskId || busy || !text.trim()) return
    setBusy(true)
    if (t.runId) watchRun(t.runId)
    try {
      const reply = await AnswerQuestion(taskId, text)
      setTurns((ts) => ts.map((turn, idx) => (idx === i ? { ...turn, reply } : turn)))
    } catch (e) {
      const message = e instanceof Error ? e.message : String(e)
      setTurns((ts) =>
        ts.map((turn, idx) => (idx === i ? { ...turn, reply: { ...emptyReply(), error: message } } : turn)),
      )
    } finally {
      stopPolling()
      setLiveSession(null)
      setBusy(false)
    }
  }

  // Refusing is a separate path that never reaches an executor, so it is not
  // an answer of "no", nothing runs at all.
  async function decline(i: number) {
    const taskId = turns[i].reply?.taskId
    if (!taskId || busy) return
    setBusy(true)
    try {
      await DeclineQuestion(taskId, '')
      setTurns((ts) =>
        ts.map((turn, idx) =>
          idx === i ? { ...turn, reply: { ...emptyReply(), output: 'Declined. Nothing ran.' } } : turn,
        ),
      )
    } catch (e) {
      const message = e instanceof Error ? e.message : String(e)
      setTurns((ts) =>
        ts.map((turn, idx) => (idx === i ? { ...turn, reply: { ...emptyReply(), error: message } } : turn)),
      )
    } finally {
      setBusy(false)
    }
  }

  // Focus on mount, and again whenever something asks for the caret.
  useEffect(() => {
    inputRef.current?.focus()
  }, [focusSignal])

  return (
    <div className="chatv-split">
      <GovernorNotice governor={governor} busy={busy} />
      <section className="chatv">
      <div className="chatv__log" ref={logRef}>
        {turns.length === 0 && (
          <div className="chatv__empty">
            <p className="chatv__emptyT">Ask Hydra anything about this repo</p>
            <p className="chatv__emptyS">
              Routed like any other task. Every reply says which model answered, at which
              tier, and what it cost.
            </p>
          </div>
        )}
        {turns.map((t, i) => (
          <div key={i} className="turn">
            {/* A tier change belongs in the transcript, not a toast: a toast
                vanishes and then a different model answered for no visible
                reason. Only says what moved, the router does not record *why*,
                and inventing a reason would be worse than omitting one. */}
            {tierShift(turns, i) && (
              <div className="shift">
                <span className="shift__ico" aria-hidden="true">&#8663;</span>
                <span className="shift__txt">
                  Moved to <b>{turns[i - 1].reply!.tier > t.reply!.tier ? 'a stronger' : 'a cheaper'}</b>{' '}
                  model, T{turns[i - 1].reply!.tier} &rarr; T{t.reply!.tier}
                </span>
              </div>
            )}
            <p className="turn__you">{t.prompt}</p>
            {!t.reply && (
              <>
                <p className="turn__wait">{liveSession?.found ? 'working…' : 'routing…'}</p>
                {/* Same event stream Session's Timeline renders after the fact
                    (runlog.Append writes each event as it happens), shown
                    live here instead of making "what is it doing" a click
                    through to Session once the whole turn is already done. */}
                {i === turns.length - 1 && liveSession?.found && (
                  <Timeline entries={liveSession.timeline} />
                )}
              </>
            )}
            {t.reply?.question && (
              <Waiting
                question={t.reply.question}
                head={t.reply.head}
                busy={busy}
                onAnswer={(text) => void answer(i, text)}
                onDecline={() => void decline(i)}
              />
            )}
            {t.reply?.error && !t.reply.needsProbe && (
              <>
                <p className="turn__err">{t.reply.error}</p>
                {/* The run's full record outlives the error, a generic
                    dispatch failure must not be a dead end (#533). */}
                {t.reply.runId && <SessionLink reply={t.reply} onOpenRun={onOpenRun} />}
              </>
            )}
            {t.reply?.needsProbe && (
              <div className="turn__probe">
                <p className="turn__err">
                  {t.reply.error} Start a local model or add an API key, then try again.
                </p>
                <button className="turn__retry" onClick={() => retry(i)} disabled={busy}>
                  Check again
                </button>
              </div>
            )}
            {t.reply && !t.reply.error && (
              <>
                {t.recoveredNote ? (
                  <p className="turn__wait">{t.recoveredNote}</p>
                ) : (
                  <p className="turn__out">{t.reply.output}</p>
                )}
                {/* Which head, which tier, what it cost, this is a router, so
                    that is the part worth showing, not just the answer. */}
                <FellBack reply={t.reply} />
                <SessionLink reply={t.reply} onOpenRun={onOpenRun} />
              </>
            )}
          </div>
        ))}
      </div>

      <div className="chatv__composer">
        <div className="chatv__box">
          <textarea
            className="chatv__text"
            ref={inputRef}
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault()
                void send()
              }
            }}
            placeholder={busy ? 'working…' : 'Ask anything about this repo…'}
            rows={2}
            disabled={busy}
          />
          {/* The model control is part of the send row, not a setting buried in
              a header: choosing depth is part of asking (#520). */}
          <div className="chatv__bar">
            <ModelPicker head={head} onPick={setHead} disabled={busy} />
            <span className="chatv__hint">enter to send</span>
            <button
              className="chatv__send"
              onClick={() => void send()}
              disabled={busy || prompt.trim() === ''}
            >
              Send
            </button>
          </div>
        </div>
      </div>
      </section>

      <Cockpit
        live={liveSession}
        lastReply={lastSettled(turns)}
        runId={turns[turns.length - 1]?.runId}
      />
    </div>
  )
}

/**
 * Names the models that were asked and could not answer. Without it a reply
 * from a weak local model looks identical whether the router chose it or fell
 * back to it after the picked model failed, which is what #676 reported: a
 * T1 pick answered by a T10 head with nothing said.
 */
function FellBack({ reply }: { reply: ChatReply }) {
  const tried = reply.attempts ?? []
  if (tried.length === 0) return null
  return (
    <div className="turn__fellback">
      {tried.map((a) => (
        <p key={a.head} className="turn__fellbackRow">
          <b>{a.model || a.head}</b>
          {a.tier > 0 && ` (T${a.tier})`} could not answer: {a.reason}
        </p>
      ))}
      <p className="turn__fellbackRow">Answered by {reply.model || reply.head} instead.</p>
    </div>
  )
}

function SessionLink({ reply, onOpenRun }: { reply: ChatReply; onOpenRun: (runID: string) => void }) {
  const who = reply.model || reply.head
  return (
    <button className="turn__meta" onClick={() => onOpenRun(reply.runId)}>
      {who}
      {reply.tier > 0 && ` · T${reply.tier}`}
      {reply.durationMs > 0 && ` · ${ms(reply.durationMs)}`}
      {reply.costUsd > 0 && ` · ${usdExact(reply.costUsd)}`}
      {who ? ' · session →' : 'session →'}
    </button>
  )
}

// Built from whatever GetSession recovered rather than Chat()'s own return,
// the log has routing and cost (TimelineEntry/Agent carry both), never the
// answer text, so output is deliberately left blank instead of guessed at.
function recoveredReply(runId: string, s: SessionData): ChatReply {
  const root = s.agents[0]
  return {
    output: '',
    runId,
    head: root?.head ?? '',
    model: root?.model ?? '',
    tier: root?.tier ?? 0,
    costUsd: root?.costUsd ?? 0,
    durationMs: root?.durationMs ?? 0,
  }
}

function emptyReply(): ChatReply {
  return { output: '', head: '', model: '', tier: 0, costUsd: 0, durationMs: 0, runId: '' }
}

/** The most recent turn that actually settled, for the idle sidebar state. */
function lastSettled(turns: Turn[]): ChatReply | undefined {
  for (let i = turns.length - 1; i >= 0; i--) {
    if (turns[i].reply && !turns[i].reply!.error) return turns[i].reply
  }
  return undefined
}

/** True when this turn was answered at a different tier than the one before. */
function tierShift(turns: Turn[], i: number): boolean {
  if (i === 0) return false
  const prev = turns[i - 1].reply
  const cur = turns[i].reply
  return !!prev && !!cur && prev.tier > 0 && cur.tier > 0 && prev.tier !== cur.tier
}

/**
 * A task parked waiting on a human decision, shown inline in the transcript.
 *
 * Not a modal: a modal that vanishes leaves the task silently parked, and the
 * question is part of the conversation's record. The task stays parked until
 * it is answered or declined, there is deliberately no default action and
 * nothing that resolves on dismiss or timeout.
 */
function Waiting({
  question,
  head,
  busy,
  onAnswer,
  onDecline,
}: {
  question: string
  head: string
  busy: boolean
  onAnswer: (text: string) => void
  onDecline: () => void
}) {
  const [text, setText] = useState('')
  return (
    <div className="waiting">
      <div className="waiting__head">
        <span className="waiting__ico" aria-hidden="true">&#10073;&#10073;</span>
        <span className="waiting__lbl">Waiting on you</span>
        {head && <span className="waiting__who">{head}</span>}
      </div>
      <p className="waiting__q">{question}</p>
      <div className="waiting__row">
        <input
          className="waiting__in"
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && text.trim() && !busy) {
              e.preventDefault()
              onAnswer(text)
            }
          }}
          placeholder="Answer, and it runs"
          aria-label="Your answer"
          disabled={busy}
        />
        <button
          className="waiting__go"
          onClick={() => onAnswer(text)}
          disabled={busy || !text.trim()}
        >
          Answer
        </button>
        <button className="waiting__no" onClick={onDecline} disabled={busy}>
          Decline
        </button>
      </div>
    </div>
  )
}
