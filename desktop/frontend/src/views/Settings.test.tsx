import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { Settings } from './Settings'
import { GetSettings, SaveSettings } from '../bindings'
import type { Settings as SettingsData } from '../types'

vi.mock('../bindings', () => ({ GetSettings: vi.fn(), SaveSettings: vi.fn() }))
const mockGet = vi.mocked(GetSettings)
const mockSave = vi.mocked(SaveSettings)

function settings(over: Partial<SettingsData> = {}): SettingsData {
  return {
    path: '/home/a/.hydra/config.toml',
    readable: true,
    exists: true,
    cortex: 'anthropic/claude-sonnet-4.5',
    skills: ['code-gen'],
    piiLocalOnly: true,
    strictEgress: true,
    capturePayloads: false,
    payloadBudgetMb: 0,
    captureEmbeddings: false,
    embedModel: '',
    embedBudgetMb: 0,
    cacheAnswers: false,
    cacheThreshold: 0,
    cacheBudgetMb: 0,
    exploreRate: 0,
    openRouterModels: [],
    ...over,
  }
}

beforeEach(() => {
  mockGet.mockResolvedValue(settings())
  mockSave.mockImplementation((s) => Promise.resolve(s))
})
afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

const toggle = (label: RegExp) =>
  screen.getByRole('checkbox', { name: label }) as HTMLInputElement

describe('the settings form', () => {
  it('reads the machine rather than showing blanks', async () => {
    render(<Settings />)
    await waitFor(() => expect(toggle(/PII to a local head/)).toBeChecked())
    expect(toggle(/Keep prompt and response text/)).not.toBeChecked()
    expect(screen.getByText('/home/a/.hydra/config.toml')).toBeInTheDocument()
  })

  // Nothing to save is not a save: an always-live button invites a write that
  // changes nothing and rewrites the file anyway.
  it('offers nothing to save until something changed', async () => {
    render(<Settings />)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled())

    fireEvent.click(toggle(/Keep prompt and response text/))
    expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled()
  })

  it('sends every switch, not only the one that changed', async () => {
    render(<Settings />)
    await waitFor(() => expect(toggle(/PII to a local head/)).toBeChecked())

    fireEvent.click(toggle(/Serve an answer from a matching earlier question/))
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(mockSave).toHaveBeenCalledTimes(1))
    const sent = mockSave.mock.calls[0][0]
    expect(sent.cacheAnswers).toBe(true)
    // The ones it did not touch have to go too, or saving one switch resets
    // the rest to whatever the form defaulted to.
    expect(sent.piiLocalOnly).toBe(true)
    expect(sent.strictEgress).toBe(true)
    expect(sent.cortex).toBe('anthropic/claude-sonnet-4.5')
  })

  it('discards back to what is on disk', async () => {
    render(<Settings />)
    await waitFor(() => expect(toggle(/PII to a local head/)).toBeChecked())

    fireEvent.click(toggle(/PII to a local head/))
    expect(toggle(/PII to a local head/)).not.toBeChecked()

    fireEvent.click(screen.getByRole('button', { name: 'Discard' }))
    expect(toggle(/PII to a local head/)).toBeChecked()
    expect(mockSave).not.toHaveBeenCalled()
  })

  // A budget for a store nothing writes to is noise, and worse, it reads as a
  // setting that is in force.
  it('shows a budget only while the thing it bounds is on', async () => {
    render(<Settings />)
    await waitFor(() => expect(toggle(/Keep prompt and response text/)).toBeInTheDocument())
    expect(screen.queryByLabelText('Payload budget')).not.toBeInTheDocument()

    fireEvent.click(toggle(/Keep prompt and response text/))
    expect(screen.getByLabelText('Payload budget')).toBeInTheDocument()
  })

  // The backend is the authority on what was stored: it refuses values the
  // router would ignore, and the form must show what landed, not what was typed.
  it('shows what the backend stored, not what was typed', async () => {
    mockSave.mockResolvedValue(settings({ cacheAnswers: false, error: 'cache threshold must be between 0 and 1, got 1.5' }))
    render(<Settings />)
    await waitFor(() => expect(toggle(/Serve an answer/)).toBeInTheDocument())

    fireEvent.click(toggle(/Serve an answer/))
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    expect(await screen.findByText(/That wasn't saved/)).toBeInTheDocument()
    expect(screen.getByText(/cache threshold must be between 0 and 1/)).toBeInTheDocument()
    expect(toggle(/Serve an answer/)).not.toBeChecked()
  })
})

describe('a config the backend could not read', () => {
  // Rendering defaults over an unparseable file invites a save that destroys
  // the only record of what the user meant.
  it('reports it and offers no form at all', async () => {
    mockGet.mockResolvedValue(
      settings({ readable: false, error: 'config …/config.toml is not readable: toml: line 5' }),
    )
    render(<Settings />)

    expect(await screen.findByText(/Hydra can't read this config/)).toBeInTheDocument()
    expect(screen.getByText(/toml: line 5/)).toBeInTheDocument()
    expect(screen.queryByRole('checkbox')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument()
  })

  it('says a failed read is a failed read', async () => {
    mockGet.mockRejectedValue(new Error('the backend went away'))
    render(<Settings />)

    expect(await screen.findByText(/Couldn't read the config/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Try again' })).toBeInTheDocument()
  })
})

describe('a machine that has never been configured', () => {
  it('says these are defaults rather than implying a file', async () => {
    mockGet.mockResolvedValue(settings({ exists: false, cortex: '', skills: [] }))
    render(<Settings />)

    expect(await screen.findByText(/not written yet, these are the defaults/)).toBeInTheDocument()
    // and it is still editable: saving is how the first config gets written.
    fireEvent.click(toggle(/Keep prompt and response text/))
    expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled()
  })
})
