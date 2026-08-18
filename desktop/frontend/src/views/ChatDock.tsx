import { useEffect, useRef, useState } from 'react'
import { Chat, ChatEnums, GetSession, NewRunID } from '../bindings'
import type { ChatReply, Session as SessionData } from '../types'
import { ms, usdExact } from '../format'
import { Timeline } from './Session'

// Matches App.tsx's LIVE_MS — same reasoning: this is "what is happening now",
// not a retrospective read.
const POLL_MS = 2000

// Turn history survives a reload via sessionStorage — cleared when the window
// closes, which is the lifetime a still-open chat already implies. Own key so
// a future view's persistence cannot collide with it.
const TURNS_KEY = 'hydra.chatDock.turns'

interface Turn {
  prompt: string
  runId: string
  reply?: ChatReply
  // Set instead of trusting `reply` at face value when this turn's outcome
  // was reconstructed from the run log after a reload rather than returned by
  // Chat() itself. runlog never inlines model output (its own doc says so),
  // so a recovered turn can carry routing/cost but never the answer text —
  // this is what says so, rather than rendering a blank reply as if real.
  recoveredNote?: string
}

/**
 * Persistent dock, collapsed to a bar by default.
 *
 * Never a permanently-large panel: splitting attention between a chat and the
 * view it is about costs more than it gives (Sweller/Tarmizi's split-attention
 * effect), so it opens on engagement and closes again.
 *
 * open/onOpenChange are owned by App, not local state — other views (Fleet's
 * empty state, #422) need to open this dock, and a callback into a sibling
 * component isn't a thing React has; lifting the one bit of state that
 * matters is. focusSignal is a counter App bumps every time something asks
 * this dock to take focus, so "open it and focus the input" works even when
 * the dock happens to already be open.
 */
export function ChatDock({
  onOpenRun,
  open,
  onOpenChange,
  focusSignal,
}: {
  onOpenRun: (runID: string) => void
  open: boolean
  onOpenChange: (open: boolean) => void
  focusSignal: number
}) {
  const [prompt, setPrompt] = useState('')
  const [enumKey, setEnumKey] = useState('')
  const [enums, setEnums] = useState<string[]>([])
  const [turns, setTurns] = useState<Turn[]>([])
  const [busy, setBusy] = useState(false)
  // The one turn currently in flight, if any — Chat's own busy flag already
  // guarantees at most one, so this doesn't need to be keyed by turn index.
  const [liveSession, setLiveSession] = useState<SessionData | null>(null)
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
  // show well before the blocking Chat call returns — this is what makes a
  // chat turn narrate the way Session's own Timeline already does, instead of
  // sitting behind a spinner until the whole thing finishes (#513).
  function watchRun(runId: string) {
    setLiveSession(null)
    const poll = () => void GetSession(runId).then(setLiveSession).catch(() => {})
    poll()
    pollRef.current = setInterval(poll, POLL_MS)
  }

  // Reattaches to a run this page did not start watching — either a reload
  // caught it mid-dispatch, or the log needs one read to learn it already
  // finished. chat.go's dispatch runs on its own context, unaffected by the
  // frontend reloading (#533), so the run itself is never actually lost —
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
            ? 'Finished while this window was reloading — the run record was kept, not the answer text.'
            : "Couldn't confirm this task's status after reload — check Fleet."
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
      /* Storage can be full or disabled; losing persistence must not break the dock. */
    }
  }, [turns])

  useEffect(() => {
    ChatEnums().then(setEnums).catch(() => {
      /* The picker degrades to auto-routing only; not worth surfacing. */
    })
  }, [])

  useEffect(() => {
    logRef.current?.scrollTo({ top: logRef.current.scrollHeight })
  }, [turns])

  async function send() {
    const p = prompt.trim()
    if (!p || busy) return
    setPrompt('')
    setBusy(true)
    // Minted before dispatching (not read off the reply) so watchRun can
    // start polling immediately — Chat won't resolve until the whole
    // dispatch finishes, but the run's log exists from the moment it starts.
    const runId = await NewRunID()
    setTurns((t) => [...t, { prompt: p, runId }])
    watchRun(runId)
    try {
      const reply = await Chat(p, enumKey, runId)
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

  // No heads discoverable at all — dispatch.New already probes fresh on every
  // call, so retrying the same prompt IS the "look again" action, not a
  // separate one. Targets a turn by index and reuses its own stored prompt
  // rather than the (already-cleared) input state. A fresh run id, same as
  // any other dispatch attempt — the failed attempt's log stays exactly what
  // it was, a probe that found nothing.
  async function retry(i: number) {
    if (busy) return
    const p = turns[i].prompt
    setBusy(true)
    const runId = await NewRunID()
    setTurns((t) => t.map((turn, idx) => (idx === i ? { ...turn, runId } : turn)))
    watchRun(runId)
    try {
      const reply = await Chat(p, enumKey, runId)
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

  // Fires on open (the dock's own toggle button included, not just external
  // callers) and again on focusSignal so "already open, asked to start a
  // task again" still moves focus back to the input.
  useEffect(() => {
    if (open) inputRef.current?.focus()
  }, [focusSignal, open])

  if (!open) {
    return (
      <button className="dock dock--closed" onClick={() => onOpenChange(true)}>
        Ask Hydra
        {turns.length > 0 && <span className="dock__count">{turns.length}</span>}
      </button>
    )
  }

  return (
    <section className="dock dock--open">
      <header className="dock__head">
        <span className="dock__title">Ask Hydra</span>
        <select
          className="dock__enum"
          value={enumKey}
          onChange={(e) => setEnumKey(e.target.value)}
          aria-label="Routing"
        >
          {/* Auto is the absence of an enum, not one of them. */}
          <option value="">auto-route</option>
          {enums.map((k) => (
            <option key={k} value={k}>
              {k}
            </option>
          ))}
        </select>
        <button className="dock__close" onClick={() => onOpenChange(false)} aria-label="Collapse">
          ×
        </button>
      </header>

      <div className="dock__log" ref={logRef}>
        {turns.length === 0 && <p className="dock__hint">Routed like any other task. Replies say which head answered.</p>}
        {turns.map((t, i) => (
          <div key={i} className="turn">
            <p className="turn__you">{t.prompt}</p>
            {!t.reply && (
              <>
                <p className="turn__wait">{liveSession?.found ? 'working…' : 'routing…'}</p>
                {/* Same event stream Session's Timeline renders after the fact
                    (runlog.Append writes each event as it happens) — shown
                    live here instead of making "what is it doing" a click
                    through to Session once the whole turn is already done. */}
                {i === turns.length - 1 && liveSession?.found && (
                  <Timeline entries={liveSession.timeline} />
                )}
              </>
            )}
            {t.reply?.error && !t.reply.needsProbe && (
              <>
                <p className="turn__err">{t.reply.error}</p>
                {/* The run's full record outlives the error — a generic
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
                {/* Which head, which tier, what it cost — this is a router, so
                    that is the part worth showing, not just the answer. */}
                <SessionLink reply={t.reply} onOpenRun={onOpenRun} />
              </>
            )}
          </div>
        ))}
      </div>

      <div className="dock__input">
        <textarea
          ref={inputRef}
          value={prompt}
          onChange={(e) => setPrompt(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && !e.shiftKey) {
              e.preventDefault()
              void send()
            }
          }}
          placeholder={busy ? 'routing…' : 'Ask anything — enter to send, shift+enter for a newline'}
          rows={2}
          disabled={busy}
        />
      </div>
    </section>
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

// Built from whatever GetSession recovered rather than Chat()'s own return —
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
