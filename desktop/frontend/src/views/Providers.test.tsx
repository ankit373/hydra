import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { AddKey, Providers } from './Providers'
import {
  GetProviderKeys,
  RemoveProviderKey,
  SaveProviderKey,
  TestProviderKey,
  openExternal,
} from '../bindings'
import type { ProviderKey, ProviderKeys } from '../types'

vi.mock('../bindings', () => ({
  GetProviderKeys: vi.fn(),
  SaveProviderKey: vi.fn(),
  RemoveProviderKey: vi.fn(),
  TestProviderKey: vi.fn(),
  openExternal: vi.fn(),
}))
const mockGet = vi.mocked(GetProviderKeys)
const mockSave = vi.mocked(SaveProviderKey)
const mockRemove = vi.mocked(RemoveProviderKey)
const mockTest = vi.mocked(TestProviderKey)

const row = (provider: string, label: string, over: Partial<ProviderKey> = {}): ProviderKey => ({
  provider,
  label,
  keyUrl: `https://${provider}.example/keys`,
  envVar: `${provider.toUpperCase()}_API_KEY`,
  source: '',
  saved: false,
  ...over,
})

const keys = (over: Partial<Record<string, Partial<ProviderKey>>> = {}): ProviderKeys => ({
  store: 'the macOS Keychain',
  keys: [
    row('anthropic', 'Anthropic (Claude)', over.anthropic),
    row('openai', 'OpenAI (ChatGPT)', over.openai),
  ],
})

const PASTED = 'sk-pasted-0123456789abcdWXYZ'

beforeEach(() => {
  mockGet.mockResolvedValue(keys({ anthropic: { source: 'stored', hint: 'AB12', saved: true } }))
  mockSave.mockResolvedValue(keys({ anthropic: { source: 'stored', hint: 'AB12', saved: true }, openai: { source: 'stored', hint: 'WXYZ', saved: true } }))
  mockRemove.mockResolvedValue(keys())
  mockTest.mockResolvedValue({ ok: true, accepted: true, model: 'gpt-4o' })
})
afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('the providers section', () => {
  it('says where keys live and shows only the last four characters', async () => {
    render(<Providers />)
    expect(await screen.findByText('Saved, ending in AB12')).toBeInTheDocument()
    expect(screen.getByText(/the macOS Keychain/)).toBeInTheDocument()
    expect(screen.getByText('Not added')).toBeInTheDocument()
  })

  it('saves a pasted key, checks it with one call, and does not keep it on screen', async () => {
    const onChange = vi.fn()
    render(<Providers onChange={onChange} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Add key' }))

    const input = screen.getByLabelText('OpenAI (ChatGPT) key') as HTMLInputElement
    expect(input.type).toBe('password')
    fireEvent.change(input, { target: { value: PASTED } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    expect(await screen.findByText('Works. gpt-4o answered.')).toBeInTheDocument()
    expect(mockSave).toHaveBeenCalledWith('openai', PASTED)
    expect(mockTest).toHaveBeenCalledWith('openai')
    expect(onChange).toHaveBeenCalled()
    expect(screen.queryByDisplayValue(PASTED)).not.toBeInTheDocument()
    expect(screen.getByText('Saved, ending in WXYZ')).toBeInTheDocument()
  })

  it('shows a refusal and keeps the field open to correct it', async () => {
    mockSave.mockResolvedValue({ ...keys(), error: 'The key was not saved: the key contains spaces' })
    render(<Providers />)
    fireEvent.click(await screen.findByRole('button', { name: 'Add key' }))
    fireEvent.change(screen.getByLabelText('OpenAI (ChatGPT) key'), { target: { value: 'sk bad' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('contains spaces')
    expect(mockTest).not.toHaveBeenCalled()
    expect(screen.getByLabelText('OpenAI (ChatGPT) key')).toBeInTheDocument()
  })

  it('tests and removes a saved key', async () => {
    mockTest.mockResolvedValue({ ok: false, accepted: false, error: 'The provider rejected this key.' })
    render(<Providers />)
    fireEvent.click(await screen.findByRole('button', { name: 'Test' }))
    expect(await screen.findByText('The provider rejected this key.')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Remove' }))
    await waitFor(() => expect(mockRemove).toHaveBeenCalledWith('anthropic'))
    expect(await screen.findAllByText('Not added')).toHaveLength(2)
  })

  // The exported key is the one in use; a test would report on it instead.
  it('does not test a pasted key that an exported one shadows, and still offers to remove it', async () => {
    mockSave.mockResolvedValue(keys({ openai: { source: 'environment', hint: 'ABCD', saved: true } }))
    render(<Providers />)
    fireEvent.click((await screen.findAllByRole('button', { name: 'Add key' }))[0])
    fireEvent.change(screen.getByLabelText('OpenAI (ChatGPT) key'), { target: { value: PASTED } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    expect(await screen.findByText(/was not tested/)).toBeInTheDocument()
    expect(mockTest).not.toHaveBeenCalled()
    expect(screen.getAllByRole('button', { name: 'Remove' })).toHaveLength(1)
  })

  it('opens the provider page in the browser, not in the app window', async () => {
    render(<Providers />)
    fireEvent.click(await screen.findByRole('button', { name: 'Add key' }))
    fireEvent.click(screen.getByRole('button', { name: 'Get a key' }))
    expect(openExternal).toHaveBeenCalledWith('https://openai.example/keys')
  })
})

describe('the first-run key form', () => {
  it('reports success only once the provider has answered', async () => {
    const onWorking = vi.fn()
    render(<AddKey onWorking={onWorking} />)
    fireEvent.change(await screen.findByRole('combobox'), { target: { value: 'openai' } })
    fireEvent.change(screen.getByPlaceholderText('Paste your key'), { target: { value: PASTED } })
    fireEvent.click(screen.getByRole('button', { name: 'Save and check' }))

    expect(await screen.findByText('Works. gpt-4o answered.')).toBeInTheDocument()
    expect(onWorking).toHaveBeenCalledTimes(1)
  })

  it('stays put with the reason when the key does not work', async () => {
    mockTest.mockResolvedValue({ ok: false, accepted: false, error: 'The provider rejected this key.' })
    const onWorking = vi.fn()
    render(<AddKey onWorking={onWorking} />)
    fireEvent.change(await screen.findByRole('combobox'), { target: { value: 'openai' } })
    fireEvent.change(screen.getByPlaceholderText('Paste your key'), { target: { value: PASTED } })
    fireEvent.click(screen.getByRole('button', { name: 'Save and check' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('rejected this key')
    expect(onWorking).not.toHaveBeenCalled()
  })

  // The provider took the key; an empty account is for the sample to report.
  it('moves on when the key is accepted but the account is out of credit', async () => {
    mockTest.mockResolvedValue({ ok: false, accepted: true, error: 'The key works, but the account cannot take requests right now' })
    const onWorking = vi.fn()
    render(<AddKey onWorking={onWorking} />)
    fireEvent.change(await screen.findByRole('combobox'), { target: { value: 'openai' } })
    fireEvent.change(screen.getByPlaceholderText('Paste your key'), { target: { value: PASTED } })
    fireEvent.click(screen.getByRole('button', { name: 'Save and check' }))

    await waitFor(() => expect(onWorking).toHaveBeenCalledTimes(1))
  })

  it('links to where the chosen provider issues keys', async () => {
    render(<AddKey onWorking={() => {}} />)
    fireEvent.change(await screen.findByRole('combobox'), { target: { value: 'anthropic' } })
    fireEvent.click(screen.getByRole('button', { name: 'Open Anthropic' }))
    expect(openExternal).toHaveBeenCalledWith('https://anthropic.example/keys')
  })
})
