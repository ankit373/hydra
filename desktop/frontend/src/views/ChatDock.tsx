import { useEffect, useRef, useState } from 'react'
import { Chat, ChatEnums, GetSession, NewRunID } from '../bindings'
import type { ChatReply, Session as SessionData } from '../types'
import { ms, usdExact } from '../format'
import { Timeline } from './Session'

// Matches App.tsx's LIVE_MS — same reasoning: this is "what is happening now",
// not a retrospective read.
const POLL_MS = 2000

interface Turn {
  prompt: string
  runId: string
  reply?: ChatReply
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
            {t.reply?.error && !t.reply.needsProbe && <p className="turn__err">{t.reply.error}</p>}
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
                <p className="turn__out">{t.reply.output}</p>
                {/* Which head, which tier, what it cost — this is a router, so
                    that is the part worth showing, not just the answer. */}
                <button className="turn__meta" onClick={() => onOpenRun(t.reply!.runId)}>
                  {t.reply.model || t.reply.head}
                  {t.reply.tier > 0 && ` · T${t.reply.tier}`}
                  {t.reply.durationMs > 0 && ` · ${ms(t.reply.durationMs)}`}
                  {t.reply.costUsd > 0 && ` · ${usdExact(t.reply.costUsd)}`}
                  {' · session →'}
                </button>
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

function emptyReply(): ChatReply {
  return { output: '', head: '', model: '', tier: 0, costUsd: 0, durationMs: 0, runId: '' }
}
