import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import { Code, total } from './Code'
import { GetDiff } from '../bindings'
import type { Diff, DiffLine, Edit } from '../types'

vi.mock('../bindings', () => ({ GetDiff: vi.fn() }))
const mockDiff = vi.mocked(GetDiff)

function edit(over: Partial<Edit> = {}): Edit {
  return { file: 'internal/auth/token.go', ts: '', ref: 'r1', added: 12, removed: 4, ...over }
}

function line(over: Partial<DiffLine> = {}): DiffLine {
  return { op: ' ', text: 'context', oldLine: 1, newLine: 1, ...over }
}

function diff(lines: DiffLine[]): Diff {
  return { file: 'internal/auth/token.go', found: true, lines, added: 1, removed: 1 }
}

beforeEach(() => {
  mockDiff.mockResolvedValue(diff([line()]))
})
afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('the change total', () => {
  it('sums across every file, which the per-file column never did', () => {
    const edits = [edit(), edit({ file: 'b.go', added: 3, removed: 1 })]
    expect(total(edits, 'added')).toBe(15)
    expect(total(edits, 'removed')).toBe(5)
  })

  it('counts a missing number as zero rather than NaN', () => {
    expect(total([edit({ added: undefined as unknown as number })], 'added')).toBe(0)
  })

  it('shows the total and pluralises the file count', async () => {
    render(<Code runID="r" edits={[edit(), edit({ file: 'b.go' })]} />)
    expect(await screen.findByText('2 files')).toBeInTheDocument()
    cleanup()
    render(<Code runID="r" edits={[edit()]} />)
    expect(await screen.findByText('1 file')).toBeInTheDocument()
  })
})

describe('intra-line detail', () => {
  it('emphasises only the part that moved', async () => {
    mockDiff.mockResolvedValue(
      diff([
        line({ op: '-', text: 'key := os.Getenv("SECRET")', spans: [{ start: 7, end: 26 }] }),
        line({ op: '+', text: 'key := loadSigningKey()', spans: [{ start: 7, end: 23 }] }),
      ]),
    )
    render(<Code runID="r" edits={[edit()]} />)

    const marks = await screen.findAllByText(/os\.Getenv|loadSigningKey/)
    expect(marks.length).toBeGreaterThan(0)
    // The shared prefix must stay outside the emphasis.
    for (const m of marks) {
      expect(m.tagName).toBe('MARK')
      expect(m.textContent).not.toContain('key :=')
    }
  })

  it('renders a line with no spans as plain text', async () => {
    mockDiff.mockResolvedValue(diff([line({ op: '+', text: 'brand new line' })]))
    const { container } = render(<Code runID="r" edits={[edit()]} />)

    expect(await screen.findByText('brand new line')).toBeInTheDocument()
    expect(container.querySelectorAll('mark')).toHaveLength(0)
  })

  // The backend bounds spans, but a stale bridge must degrade to readable text
  // rather than throwing and blanking the pane.
  it('falls back to plain text for a span outside the line', async () => {
    mockDiff.mockResolvedValue(
      diff([line({ op: '+', text: 'short', spans: [{ start: 2, end: 999 }] })]),
    )
    const { container } = render(<Code runID="r" edits={[edit()]} />)

    expect(await screen.findByText(/short/)).toBeInTheDocument()
    expect(container.querySelectorAll('mark')).toHaveLength(0)
  })

  it('keeps the text intact across several spans', async () => {
    mockDiff.mockResolvedValue(
      diff([
        line({
          op: '-',
          text: 'return f([]byte(k))',
          spans: [
            { start: 9, end: 16 },
            { start: 18, end: 19 },
          ],
        }),
      ]),
    )
    const { container } = render(<Code runID="r" edits={[edit()]} />)
    await screen.findAllByText(/return/)

    const row = container.querySelector('.dl__text')
    expect(row?.textContent).toBe('return f([]byte(k))')
    expect(container.querySelectorAll('mark')).toHaveLength(2)
  })
})

describe('when there is nothing to show', () => {
  it('says the run changed no files', () => {
    render(<Code runID="r" edits={[]} />)
    expect(screen.getByText(/changed no files/i)).toBeInTheDocument()
  })

  it('says why a diff is unavailable instead of rendering blank', async () => {
    mockDiff.mockResolvedValue({
      file: 'a.go',
      found: false,
      reason: 'snapshot is no longer on disk',
      lines: [],
      added: 0,
      removed: 0,
    })
    render(<Code runID="r" edits={[edit()]} />)
    expect(await screen.findByText(/snapshot is no longer on disk/)).toBeInTheDocument()
  })
})
