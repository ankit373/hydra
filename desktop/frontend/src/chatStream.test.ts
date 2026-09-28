import { describe, expect, it } from 'vitest'
import {
  abandoned,
  applyStreamEvent,
  emptyStream,
  liveText,
  type ChatStreamEvent,
} from './chatStream'

const RUN = 'run-1'

const delta = (
  text: string,
  offset: number,
  over: Partial<ChatStreamEvent> = {},
): ChatStreamEvent => ({
  runID: RUN,
  kind: 'delta',
  head: 'ollama/qwen3',
  tier: 10,
  text,
  offset,
  recoverable: false,
  ...over,
})

const apply = (evs: ChatStreamEvent[]) =>
  evs.reduce(applyStreamEvent, emptyStream(RUN))

describe('applyStreamEvent', () => {
  it('grows the open message as deltas arrive', () => {
    const s = apply([delta('hel', 0), delta('lo ', 3), delta('world', 6)])
    expect(liveText(s)).toBe('hello world')
    expect(s.gap).toBe(false)
  })

  // The event the view already rendered. Wails can deliver one twice, and
  // appending it would show the text twice with nothing to say it was wrong.
  it('ignores a delta it has already rendered', () => {
    const s = apply([delta('hello', 0), delta('hello', 0), delta(' there', 5)])
    expect(liveText(s)).toBe('hello there')
  })

  // The opposite case, and the one that must never be papered over: appending
  // text that starts further along than what we hold splices a hole into the
  // answer and renders it as if the head wrote it.
  it('reports a gap rather than splicing over a missed event', () => {
    const s = apply([delta('hello', 0), delta('world', 99)])
    expect(s.gap).toBe(true)
    expect(liveText(s)).toBe('hello')
  })

  // The Go side counts bytes. Measuring the held text in UTF-16 code units
  // would disagree on the first non-ASCII character and report a phantom gap.
  it('places a delta after a multi-byte rune', () => {
    // 6, written out: 'héllo' is five characters and six bytes, and the Go
    // side counted bytes. Calling byteLength here would move with the code
    // under test and agree with it however wrong both were.
    const s = apply([delta('héllo', 0), delta('!', 6)])
    expect(liveText(s)).toBe('héllo!')
    expect(s.gap).toBe(false)
  })

  it('opens an attempt for a delta that arrives before its started event', () => {
    const s = apply([delta('orphan', 0)])
    expect(liveText(s)).toBe('orphan')
  })

  // An event from a dispatch that has already been replaced on screen.
  it('ignores an event belonging to another run', () => {
    const s = apply([delta('mine', 0), delta('theirs', 4, { runID: 'run-2' })])
    expect(liveText(s)).toBe('mine')
  })
})

describe('a fallback', () => {
  const chain: ChatStreamEvent[] = [
    {
      runID: RUN,
      kind: 'started',
      head: 'openrouter/gpt',
      tier: 4,
      offset: 0,
      recoverable: true,
    },
    delta('half an ans', 0, { head: 'openrouter/gpt' }),
    {
      runID: RUN,
      kind: 'failed',
      head: 'openrouter/gpt',
      tier: 4,
      reason: '429 rate limited',
      spanID: 'span-a',
      offset: 11,
      recoverable: true,
    },
    {
      runID: RUN,
      kind: 'started',
      head: 'claude',
      tier: 1,
      offset: 0,
      recoverable: true,
    },
    delta('the real answer', 0, { head: 'claude' }),
  ]

  it('replaces the abandoned partial rather than appending to it', () => {
    expect(liveText(apply(chain))).toBe('the real answer')
  })

  it('keeps the abandoned attempt, with its reason and span', () => {
    const gone = abandoned(apply(chain))
    expect(gone).toHaveLength(1)
    expect(gone[0].head).toBe('openrouter/gpt')
    expect(gone[0].text).toBe('half an ans')
    expect(gone[0].abandonedReason).toBe('429 rate limited')
    // What makes it reachable afterwards as `hyctl trace view <run> --span <id>`.
    expect(gone[0].spanID).toBe('span-a')
    expect(gone[0].recoverable).toBe(true)
  })

  // Every head failed, and the last one had written something first. The
  // partial must not stand as the answer: nobody is going to finish it, and
  // rendering it plain is indistinguishable from a reply that succeeded.
  it('shows no answer when the final attempt was abandoned mid-text', () => {
    const s = apply([
      {
        runID: RUN,
        kind: 'started',
        head: 'solo',
        tier: 4,
        offset: 0,
        recoverable: true,
      },
      delta('a partial nobody will finish', 0, { head: 'solo' }),
      {
        runID: RUN,
        kind: 'failed',
        head: 'solo',
        tier: 4,
        reason: 'timeout',
        offset: 27,
        recoverable: true,
      },
    ])
    expect(liveText(s)).toBe('')
    // Kept, though: it is what the collapsed summary expands to show.
    expect(abandoned(s)[0].text).toBe('a partial nobody will finish')
  })

  // A head that streamed nothing before failing has no partial to expand, and
  // the view must not offer to show an empty one.
  it('records an attempt that failed before writing anything', () => {
    const s = apply([
      {
        runID: RUN,
        kind: 'started',
        head: 'dead',
        tier: 3,
        offset: 0,
        recoverable: true,
      },
      {
        runID: RUN,
        kind: 'failed',
        head: 'dead',
        tier: 3,
        reason: 'connection refused',
        offset: 0,
        recoverable: true,
      },
    ])
    expect(abandoned(s)[0].text).toBe('')
    expect(liveText(s)).toBe('')
  })
})

describe('a non-streaming head', () => {
  // executor.Stream delivers the whole output as one delta, which is what lets
  // the view be written once rather than growing a second branch.
  it('renders as a single delta', () => {
    const s = apply([
      {
        runID: RUN,
        kind: 'started',
        head: 'replicate',
        tier: 5,
        offset: 0,
        recoverable: false,
      },
      delta('the entire answer at once', 0, { head: 'replicate' }),
    ])
    expect(liveText(s)).toBe('the entire answer at once')
    expect(s.gap).toBe(false)
  })
})
