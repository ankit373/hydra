import { useEffect, useRef, useState } from 'react'
import { Chat, ChatEnums } from '../bindings'
import type { ChatReply } from '../types'
import { ms, usdExact } from '../format'

interface Turn {
  prompt: string
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
  const logRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLTextAreaElement>(null)

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
    setTurns((t) => [...t, { prompt: p }])
    try {
      const reply = await Chat(p, enumKey)
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
            {!t.reply && <p className="turn__wait">routing…</p>}
            {t.reply?.error && <p className="turn__err">{t.reply.error}</p>}
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
