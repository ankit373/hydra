import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { FirstRun } from './FirstRun'
import { Chat, GetProviderKeys, SaveProviderKey, TestProviderKey, openExternal } from '../bindings'
import type { ChatReply, ProviderKeys } from '../types'

vi.mock('../bindings', () => ({
  Chat: vi.fn(),
  GetProviderKeys: vi.fn(),
  SaveProviderKey: vi.fn(),
  TestProviderKey: vi.fn(),
  openExternal: vi.fn(),
}))

const providers: ProviderKeys = {
  store: 'the macOS Keychain',
  keys: [{ provider: 'openai', label: 'OpenAI (ChatGPT)', keyUrl: 'https://o.example', envVar: 'OPENAI_API_KEY', source: '', saved: false }],
}

const answer: ChatReply = {
  output: 'I can summarise documents and draft emails.',
  head: 'env/openai',
  model: 'GPT-4o',
  tier: 4,
  costUsd: 0,
  durationMs: 1800,
  runId: 'run-1',
}

beforeEach(() => {
  vi.mocked(GetProviderKeys).mockResolvedValue(providers)
  vi.mocked(SaveProviderKey).mockResolvedValue(providers)
  vi.mocked(TestProviderKey).mockResolvedValue({ ok: true, accepted: true, model: 'gpt-4o' })
  vi.mocked(Chat).mockResolvedValue(answer)
})
afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('first run', () => {
  it('goes from no model to a first answer by pasting a key', async () => {
    const onRecheck = vi.fn().mockResolvedValue(true)
    const onDone = vi.fn()
    render(<FirstRun onRecheck={onRecheck} onDone={onDone} />)

    fireEvent.click(screen.getByRole('button', { name: 'Add a key' }))
    fireEvent.change(await screen.findByRole('combobox'), { target: { value: 'openai' } })
    fireEvent.change(screen.getByPlaceholderText('Paste your key'), { target: { value: 'sk-0123456789abcdef' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save and check' }))

    fireEvent.click(await screen.findByRole('button', { name: 'Send' }))
    expect(await screen.findByText(answer.output)).toBeInTheDocument()
    expect(screen.getByText(/Answered by GPT-4o/)).toBeInTheDocument()
    expect(onRecheck).toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: 'Start using Hydra' }))
    expect(onDone).toHaveBeenCalled()
  })

  it('guides a local model without a terminal and says when none is found yet', async () => {
    const onRecheck = vi.fn().mockResolvedValueOnce(false).mockResolvedValueOnce(true)
    render(<FirstRun onRecheck={onRecheck} onDone={() => {}} />)

    fireEvent.click(screen.getByRole('button', { name: 'Set up a local model' }))
    fireEvent.click(screen.getByRole('button', { name: 'Open ollama.com' }))
    expect(openExternal).toHaveBeenCalledWith('https://ollama.com/download')
    expect(screen.queryByText(/terminal|command/i)).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Check again' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('No model found yet')

    fireEvent.click(screen.getByRole('button', { name: 'Check again' }))
    expect(await screen.findByRole('button', { name: 'Send' })).toBeInTheDocument()
  })

  it('can always be left without an answer', () => {
    const onDone = vi.fn()
    render(<FirstRun onRecheck={vi.fn()} onDone={onDone} />)
    fireEvent.click(screen.getByRole('button', { name: 'Skip for now' }))
    expect(onDone).toHaveBeenCalled()
  })

  it('shows a failed sample request and lets it be retried', async () => {
    vi.mocked(Chat).mockResolvedValueOnce({ ...answer, output: '', error: 'No model is available to answer this yet.' })
    render(<FirstRun onRecheck={vi.fn().mockResolvedValue(true)} onDone={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'Set up a local model' }))
    fireEvent.click(screen.getByRole('button', { name: 'Check again' }))

    fireEvent.click(await screen.findByRole('button', { name: 'Send' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('No model is available')
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(await screen.findByText(answer.output)).toBeInTheDocument()
  })
})
