import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { Models, reachClass, reachText } from './Models'
import { GetDashboard, GetModels } from '../bindings'
import type { CalibrationRow, Head, HeadPanel, Model, ModelRegistry } from '../types'

vi.mock('../bindings', () => ({ GetModels: vi.fn(), GetDashboard: vi.fn() }))
const mockModels = vi.mocked(GetModels)
const mockDash = vi.mocked(GetDashboard)

/** Heads reach the view as a prop now: it used to call probe.Run on a 5s tick. */
function panel(heads: Head[] = []): HeadPanel {
  return { heads, routable: heads.filter((h) => h.routable).length }
}

function model(over: Partial<Model> = {}): Model {
  return {
    id: 'sonnet-thinking',
    name: 'Claude Sonnet (Thinking)',
    tier: 3,
    provider: 'antigravity',
    pool: 'agy_claude',
    complexityMin: 6,
    complexityMax: 7,
    speed: 'medium_slow',
    accuracy: 'high',
    contextWindow: 200000,
    enabled: true,
    ...over,
  }
}

function registry(over: Partial<ModelRegistry> = {}): ModelRegistry {
  return {
    found: true,
    pools: [
      {
        name: 'agy_claude',
        shared: true,
        models: [model(), model({ id: 'opus-thinking', name: 'Claude Opus (Thinking)', tier: 2 })],
        observedCalls: 12,
        observedCostUsd: 0.094,
        observedTokens: 4000,
      },
    ],
    ...over,
  }
}

function cal(over: Partial<CalibrationRow> = {}): CalibrationRow {
  return { source: 'model:sonnet-thinking', domain: 'go', d: 1.42, se: 0.8, sp: 0.7, n: 62, ...over }
}

beforeEach(() => {
  mockModels.mockResolvedValue(registry())
  mockDash.mockResolvedValue({ calibration: [] } as never)
})
afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('the model list', () => {
  it('names the quota a model spends and says when that quota is shared', async () => {
    render(<Models heads={panel()} />)
    // agy_claude -> "Claude"; the raw pool key is config, not a label.
    const spend = await screen.findAllByTitle(/not a reading of the provider/i)
    expect(spend[0].textContent).toMatch(/Claude/)
    // Once per model in the pool: the fact is about the row, not a group header.
    expect(screen.getAllByText('shared quota')).toHaveLength(2)
  })

  it('does not claim a quota is shared when it has one member', async () => {
    mockModels.mockResolvedValue(
      registry({
        pools: [
          {
            name: 'local_ollama',
            shared: true,
            models: [model({ id: 'qwen', name: 'Qwen', tier: 10 })],
            observedCalls: 0,
            observedCostUsd: 0,
            observedTokens: 0,
          },
        ],
      }),
    )
    render(<Models heads={panel()} />)
    // Twice by design: the list row, and the scorecard for the selected model.
    expect(await screen.findAllByText('Qwen')).toHaveLength(2)
    // Flagged shared, but with nothing to contend against, so saying so would mislead.
    expect(screen.queryByText('shared quota')).not.toBeInTheDocument()
  })

  // Observed spend is Hydra's own log, not a provider balance. The wording and
  // the tooltip both have to stop short of implying otherwise.
  it('labels pool spend as logged, never as remaining quota', async () => {
    render(<Models heads={panel()} />)
    const spend = (await screen.findAllByTitle(/not a reading of the provider/i))[0]
    expect(spend.textContent).toMatch(/12 requests/)
    expect(spend.textContent).toMatch(/logged/)
  })

  // usdExact renders 0 as an em-dash, which turned "no cost" into "- logged".
  it('says a free pool costs nothing rather than printing a dash', async () => {
    mockModels.mockResolvedValue(
      registry({
        pools: [
          {
            name: 'local_ollama',
            shared: false,
            models: [model({ id: 'qwen', name: 'Qwen', tier: 10 })],
            observedCalls: 9,
            observedCostUsd: 0,
            observedTokens: 120,
          },
        ],
      }),
    )
    render(<Models heads={panel()} />)
    const spend = (await screen.findAllByTitle(/not a reading of the provider/i))[0]
    expect(spend.textContent).toMatch(/9 requests/)
    expect(spend.textContent).toMatch(/no cost/)
    expect(spend.textContent).not.toMatch(/logged/)
  })

  it('shows a disabled model rather than hiding it', async () => {
    mockModels.mockResolvedValue(
      registry({
        pools: [
          {
            name: 'agy_claude',
            shared: false,
            models: [model({ enabled: false })],
            observedCalls: 0,
            observedCostUsd: 0,
            observedTokens: 0,
          },
        ],
      }),
    )
    render(<Models heads={panel()} />)
    expect(await screen.findAllByText('Claude Sonnet (Thinking)')).toHaveLength(2)
    // Stated once, on the reachability line, which also gives the reason. It
    // used to be said twice, a separate badge plus that line.
    expect(screen.getByText(/switched off in models\.yaml/)).toBeInTheDocument()
  })
})

/**
 * The list sorts cheapest tier first, so the default selection is whichever
 * model that is. Every scorecard assertion below is about one named model's
 * record, so it opens that model rather than trusting the ordering.
 */
async function openScorecard(name: string) {
  fireEvent.click(await screen.findByText(name))
}

describe('the scorecard never states a score it cannot support', () => {
  it('says nothing is measured rather than showing a zero', async () => {
    render(<Models heads={panel()} />)
    expect(await screen.findByText(/absence of evidence, not a low score/i)).toBeInTheDocument()
  })

  it('always prints the outcome count beside the evidence weight', async () => {
    mockDash.mockResolvedValue({ calibration: [cal()] } as never)
    render(<Models heads={panel()} />)
    await openScorecard('Claude Sonnet (Thinking)')
    expect(await screen.findByText('1.42')).toBeInTheDocument()
    expect(screen.getByText(/62 outcomes/)).toBeInTheDocument()
  })

  // The trap from #593: a high D on a handful of samples is not a measurement.
  it('flags a strong-looking score built on too few outcomes', async () => {
    mockDash.mockResolvedValue({ calibration: [cal({ d: 2.41, n: 4 })] } as never)
    render(<Models heads={panel()} />)
    await openScorecard('Claude Sonnet (Thinking)')
    const score = await screen.findByText('2.41')
    expect(screen.getByText(/too few to trust/i)).toBeInTheDocument()
    // Muted, not green: it must not read as a finding.
    expect(score.className).toMatch(/--thin/)
    expect(score.className).not.toMatch(/--strong/)
  })

  it('grades a genuinely strong record as strong', async () => {
    mockDash.mockResolvedValue({ calibration: [cal({ d: 1.42, n: 62 })] } as never)
    render(<Models heads={panel()} />)
    await openScorecard('Claude Sonnet (Thinking)')
    expect((await screen.findByText('1.42')).className).toMatch(/--strong/)
  })

  it('grades a weak record as weak even with plenty of outcomes', async () => {
    mockDash.mockResolvedValue({ calibration: [cal({ d: 0.28, n: 40 })] } as never)
    render(<Models heads={panel()} />)
    await openScorecard('Claude Sonnet (Thinking)')
    expect((await screen.findByText('0.28')).className).toMatch(/--weak/)
  })
})

describe('when the registry cannot be read', () => {
  it('says it could not look, rather than showing an empty list as fact', async () => {
    mockModels.mockResolvedValue({ found: false, error: 'malformed models.yaml', pools: [] })
    render(<Models heads={panel()} />)
    expect(await screen.findByText(/Couldn't read the model registry/i)).toBeInTheDocument()
    expect(screen.getByText(/malformed models.yaml/)).toBeInTheDocument()
  })

  it('renders nothing at all before the first read resolves', async () => {
    let release: (r: ModelRegistry) => void = () => {}
    mockModels.mockReturnValue(new Promise<ModelRegistry>((r) => (release = r)))
    const { container } = render(<Models heads={panel()} />)
    expect(container).toBeEmptyDOMElement()
    release(registry())
    await waitFor(() => expect(container).not.toBeEmptyDOMElement())
  })
})

// The dot used to come from models.yaml's `enabled` flag, an install-specific
// default that says nothing about reachability, so a head with no API key, or
// an Ollama model whose server is down, rendered as live. The view's own
// subtitle claims "what this machine can route to".
describe('reachable now, as opposed to declared', () => {
  const model_ = model()

  it('is unknown before the probe answers, rather than guessing a dot', () => {
    expect(reachClass(model_, null)).toBe('unknown')
    expect(reachText(model_, null)).toBe('checking…')
  })

  it('renders the unknown dot, not an off one, before the probe answers', async () => {
    render(<Models heads={null} />)
    const dots = await screen.findAllByTitle('checking…')
    for (const d of dots) {
      expect(d.className).toMatch(/--unknown/)
      expect(d.className).not.toMatch(/--off/)
    }
  })

  it('is live only when a probed head for it is routable', () => {
    const heads = [{ id: model_.id, name: 'x', provider: 'agy', source: 'registry', tier: 3, capScore: 90, routable: true, localOnly: false }]
    expect(reachClass(model_, heads)).toBe('on')
    expect(reachText(model_, heads)).toBe('yes')
  })

  it('gives the reason instead of a bare no', () => {
    const heads = [{
      id: model_.id, name: 'x', provider: 'ollama', source: 'cli', tier: 10,
      capScore: 40, routable: false, localOnly: true,
      reason: 'binary only, start its local server (e.g. `ollama serve`) to route to its models',
    }]
    expect(reachClass(model_, heads)).toBe('off')
    expect(reachText(model_, heads)).toMatch(/ollama serve/)
  })

  // A model with no matching head is switched off or was not discovered, and
  // those need different fixes, so they must not collapse into one message.
  it('tells a switched-off model apart from one the scan missed', () => {
    expect(reachText(model({ enabled: false }), [])).toMatch(/switched off in models\.yaml/)
    expect(reachText(model({ enabled: true }), [])).toMatch(/last scan did not find it/)
  })

  it('reports reachability in the scorecard', async () => {
      render(<Models heads={panel()} />)
    expect(await screen.findByText('Reachable now')).toBeInTheDocument()
  })
})
