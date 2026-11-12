import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { CliTool } from './CliTool'
import { CheckHyctl, InstallHyctl } from '../bindings'

vi.mock('../bindings', () => ({ CheckHyctl: vi.fn(), InstallHyctl: vi.fn() }))

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('the optional command-line tool', () => {
  it('says the app does not need it, and installs it on request', async () => {
    vi.mocked(CheckHyctl).mockResolvedValue({ found: false, supported: true })
    vi.mocked(InstallHyctl).mockResolvedValue({ ok: true, version: 'hydra v1.6.1', log: 'installed' })
    render(<CliTool />)

    expect(await screen.findByText(/The app does not need it/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Install' }))
    expect(await screen.findByText('Installed: hydra v1.6.1')).toBeInTheDocument()
  })

  it('reports a failed install with its reason', async () => {
    vi.mocked(CheckHyctl).mockResolvedValue({ found: false, supported: true })
    vi.mocked(InstallHyctl).mockResolvedValue({ ok: false, log: '', error: 'could not download the installer' })
    render(<CliTool />)

    fireEvent.click(await screen.findByRole('button', { name: 'Install' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('could not download the installer')
  })

  it('offers no install button where it cannot run one', async () => {
    vi.mocked(CheckHyctl).mockResolvedValue({ found: false, supported: false })
    render(<CliTool />)
    expect(await screen.findByText(/npm install -g hyctl/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Install' })).not.toBeInTheDocument()
  })
})
