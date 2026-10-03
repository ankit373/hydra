import { describe, expect, it } from 'vitest'
import { graphLabel } from './graphLabel'

// The two widths the graphs actually use.
const SESSION = 24
const RUN = 12

// Real ids and paths, not invented ones: the defect was specific to the shape
// of `provider/model` and `dir/dir/file` (#1120).
const MODELS = [
  'anthropic/claude-sonnet-4.5',
  'anthropic/claude-opus-4.6',
  'google/gemini-2.5-pro',
  'ollama/qwen3:8b',
  'openai/gpt-5',
  'openrouter/meta-llama/llama-3.3-70b-instruct',
]
const PATHS = ['internal/dispatch/dispatch.go', 'internal/auth/token.go', 'a.go']

describe('a graph label fits without losing what distinguishes it', () => {
  it('never exceeds the width it was given', () => {
    for (const max of [SESSION, RUN, 8, 40]) {
      for (const s of [...MODELS, ...PATHS, 'averyveryverylongsinglesegmentname']) {
        expect(graphLabel(s, max).length, `${s} @ ${max}`).toBeLessThanOrEqual(max)
      }
    }
  })

  // The defect, in one assertion: `…ropic/claude-sonnet-4.5` cut inside
  // "anthropic", which is what made two models of one provider look like two
  // providers.
  it('never cuts inside a segment', () => {
    for (const max of [SESSION, RUN]) {
      for (const s of [...MODELS, ...PATHS]) {
        const got = graphLabel(s, max)
        if (!got.startsWith('…')) continue
        expect(got, `${s} @ ${max}`).toMatch(/^…\//)
      }
    }
  })

  it('leaves a label that already fits completely alone', () => {
    expect(graphLabel('openai/gpt-5', SESSION)).toBe('openai/gpt-5')
    expect(graphLabel('a.go', RUN)).toBe('a.go')
  })

  it('keeps the model, which is what a node is identified by', () => {
    expect(graphLabel('anthropic/claude-sonnet-4.5', SESSION)).toBe('…/claude-sonnet-4.5')
    expect(graphLabel('openrouter/meta-llama/llama-3.3-70b-instruct', SESSION))
      .toBe('…/llama-3.3-70b-instruct')
  })

  it('keeps the filename, which is what a path is identified by', () => {
    expect(graphLabel('internal/auth/token.go', RUN)).toBe('…/token.go')
    expect(graphLabel('internal/dispatch/dispatch.go', RUN)).toBe('dispatch.go')
  })

  // RunGraph truncated from the end, so every Anthropic model was
  // `anthropic/c…` and a graph of two Claude models showed one label twice.
  it('tells two models of one provider apart at the narrow width', () => {
    const a = graphLabel('anthropic/claude-sonnet-4.5', RUN)
    const b = graphLabel('anthropic/claude-opus-4.6', RUN)
    expect(a).not.toBe(b)
  })

  it('tells them apart at the wide width too', () => {
    expect(graphLabel('anthropic/claude-sonnet-4.5', SESSION))
      .not.toBe(graphLabel('anthropic/claude-opus-4.6', SESSION))
  })

  // A segment longer than the whole budget has nowhere to snap to, so the
  // start is kept: it is the part that still carries information.
  it('keeps the start when even the last segment will not fit', () => {
    expect(graphLabel('openrouter/meta-llama/llama-3.3-70b-instruct', RUN)).toBe('llama-3.3-7…')
    expect(graphLabel('averyveryverylongsinglesegmentname', RUN)).toBe('averyveryve…')
  })

  it('adds no elision to a last segment that fits on its own', () => {
    expect(graphLabel('internal/dispatch/dispatch.go', RUN)).not.toContain('…')
  })
})
