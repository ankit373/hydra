// Applying a dispatch's stream events to what the chat shows.
//
// A pure reducer rather than logic inside ChatView: placing a delta by its
// byte offset is where duplicates and gaps are told apart, and that deserves
// tests that do not need a rendered component.

/** The Wails event name. Must match api.ChatStreamEventName. */
export const CHAT_STREAM_EVENT = 'chat:stream'

/** One thing that happened while a chat dispatch ran. Mirrors api.ChatStreamEvent. */
export interface ChatStreamEvent {
  runID: string
  kind: 'started' | 'delta' | 'failed'
  head: string
  tier: number
  text?: string
  reason?: string
  spanID?: string
  offset: number
  recoverable: boolean
}

/** One head's turn at answering. The last in a run is the one in flight. */
export interface Attempt {
  head: string
  tier: number
  text: string
  spanID: string
  recoverable: boolean
  /** Set when the chain moved on. The text is a partial nobody will finish. */
  abandonedReason?: string
}

export interface LiveStream {
  runId: string
  attempts: Attempt[]
  /**
   * An event never arrived: a delta claimed to start further along than the
   * text we hold. Set rather than papering over it, because appending anyway
   * splices a hole into the answer and shows it as if the head wrote it.
   */
  gap: boolean
}

export const emptyStream = (runId: string): LiveStream => ({
  runId,
  attempts: [],
  gap: false,
})

/** The text on screen: the attempt in flight, or the last one to have run. */
export function liveText(s: LiveStream | null): string {
  if (!s || s.attempts.length === 0) return ''
  const last = s.attempts[s.attempts.length - 1]
  return last.abandonedReason ? '' : last.text
}

/** The attempts that were abandoned, in the order they were tried. */
export function abandoned(s: LiveStream | null): Attempt[] {
  return s ? s.attempts.filter((a) => a.abandonedReason !== undefined) : []
}

export function applyStreamEvent(
  s: LiveStream,
  ev: ChatStreamEvent,
): LiveStream {
  // Two runs can overlap: a reply arriving late from a previous dispatch must
  // not append itself to the answer now on screen.
  if (ev.runID !== s.runId) return s

  if (ev.kind === 'started') {
    const started: Attempt = {
      head: ev.head,
      tier: ev.tier,
      text: '',
      spanID: ev.spanID ?? '',
      recoverable: ev.recoverable,
    }
    return { ...s, attempts: [...s.attempts, started] }
  }

  // A delta before any 'started' is still an answer; open an attempt for it
  // rather than dropping output because one event went missing.
  const attempts = s.attempts.length
    ? [...s.attempts]
    : [
        {
          head: ev.head,
          tier: ev.tier,
          text: '',
          spanID: ev.spanID ?? '',
          recoverable: ev.recoverable,
        },
      ]
  const i = attempts.length - 1
  const cur = attempts[i]

  if (ev.kind === 'failed') {
    attempts[i] = {
      ...cur,
      abandonedReason: ev.reason ?? '',
      spanID: ev.spanID ?? cur.spanID,
    }
    return { ...s, attempts }
  }

  const held = byteLength(cur.text)
  if (ev.offset < held) return s; // already rendered; a replay, not more answer
  if (ev.offset > held) return { ...s, gap: true }

  attempts[i] = { ...cur, text: cur.text + (ev.text ?? '') }
  return { ...s, attempts }
}

/**
 * The offset counts bytes, because that is what the Go side counted. Using
 * string.length would drift on the first non-ASCII character and report a gap
 * that is not there.
 */
export function byteLength(s: string): number {
  return new TextEncoder().encode(s).length
}
