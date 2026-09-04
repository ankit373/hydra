import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MCPServers, scoreText, stateText } from './MCPServers'
import { GetMCPServers, SyncMCPRegistry } from '../bindings'
import type { MCPPanel, MCPServer } from '../types'

vi.mock('../bindings', () => ({ GetMCPServers: vi.fn(), SyncMCPRegistry: vi.fn() }))
const mockGet = vi.mocked(GetMCPServers)
const mockSync = vi.mocked(SyncMCPRegistry)

function server(over: Partial<MCPServer> = {}): MCPServer {
  return {
    name: 'postmark', client: 'claude-code', scope: 'user',
    command: 'npx', package: 'postmark-mcp', remote: false,
    status: 'unresolved', scored: false, score: 0, ...over,
  }
}
function panel(over: Partial<MCPPanel> = {}): MCPPanel {
  return { servers: [], scanned: true, ...over }
}

beforeEach(() => mockGet.mockResolvedValue(panel()))
afterEach(() => { cleanup(); vi.clearAllMocks() })

describe('what the absence of a sync means', () => {
  // A list of unresolved servers says nothing about those servers if nothing
  // has ever been compared against.
  it('says unresolved is the absence of a comparison, not a finding', async () => {
    mockGet.mockResolvedValue(panel({ servers: [server()] }))
    render(<MCPServers />)
    expect(await screen.findByText(/never been pulled/i)).toBeInTheDocument()
    expect(screen.getByText(/not because anything is wrong with it/i)).toBeInTheDocument()
  })

  it('names the sync date once there is one', async () => {
    mockGet.mockResolvedValue(panel({ synced: '2026-09-01T10:00:00Z' }))
    render(<MCPServers />)
    expect(await screen.findByText(/last pulled 2026-09-01/)).toBeInTheDocument()
  })
})

describe('an empty list', () => {
  it('distinguishes "none installed" from "the scan did not run"', async () => {
    render(<MCPServers />)
    expect(await screen.findByText(/no mcp servers are configured/i)).toBeInTheDocument()

    cleanup()
    mockGet.mockResolvedValue(panel({ scanned: false }))
    render(<MCPServers />)
    expect(await screen.findByText(/not a statement that none are installed/i)).toBeInTheDocument()
  })
})

describe('never stating a score it cannot support', () => {
  it('says not scored rather than drawing a zero', () => {
    expect(scoreText(server({ scored: false }))).toBe('not scored')
  })

  // The thing every other MCP directory gets wrong.
  it('says insufficient evidence rather than printing the number', () => {
    expect(scoreText(server({ scored: true, score: 71, confidence: 'insufficient_evidence' })))
      .toBe('insufficient evidence')
  })

  it('prints a supported score with its confidence', () => {
    expect(scoreText(server({ scored: true, score: 71.4, confidence: 'moderate' })))
      .toBe('71/100 · moderate')
  })
})

describe('the trust state in plain words', () => {
  it('does not leave a lifecycle state as a bare internal name', () => {
    expect(stateText(server({ state: 'provisional' }))).toBe('awaiting re-check')
    expect(stateText(server({ state: 'quarantined' }))).toBe('quarantined')
  })

  it('falls back to registry membership when there is no lifecycle state', () => {
    expect(stateText(server({ status: 'unresolved' }))).toBe('not in the registry')
    expect(stateText(server({ status: 'verified' }))).toBe('in the registry')
  })
})

describe('the typosquat signal', () => {
  it('shows the nearest identifier and how far off it is', async () => {
    mockGet.mockResolvedValue(
      panel({ servers: [server({ nearestMatch: 'postmark-mcp-official', nearestDist: 9 })] }),
    )
    render(<MCPServers />)
    expect(await screen.findByText(/looks like postmark-mcp-official/)).toBeInTheDocument()
    expect(screen.getByText(/9 characters apart/)).toBeInTheDocument()
  })
})

describe('checking the registry', () => {
  // Offering the fetch is what keeps this from telling a GUI user to go run a
  // CLI command (#452).
  it('pulls the registry and reloads', async () => {
    mockSync.mockResolvedValue({ servers: 1284 })
    render(<MCPServers />)
    await screen.findByRole('button', { name: /check the registry/i })
    // Counted relative to the mount, since StrictMode invokes the mount
    // effect twice — an absolute count asserts the harness, not the code.
    const before = mockGet.mock.calls.length
    fireEvent.click(screen.getByRole('button', { name: /check the registry/i }))

    await waitFor(() => expect(mockSync).toHaveBeenCalled())
    expect(await screen.findByText(/pulled 1284 server records/i)).toBeInTheDocument()
    expect(mockGet.mock.calls.length).toBeGreaterThan(before)
  })

  it('reports a failed pull instead of looking like it worked', async () => {
    mockSync.mockResolvedValue({ servers: 0, error: 'registry unreachable' })
    render(<MCPServers />)
    await screen.findByRole('button', { name: /check the registry/i })
    const before = mockGet.mock.calls.length
    fireEvent.click(screen.getByRole('button', { name: /check the registry/i }))

    expect(await screen.findByText(/registry unreachable/)).toBeInTheDocument()
    // A failed pull must not trigger a reload that implies new data arrived.
    expect(mockGet.mock.calls.length).toBe(before)
  })
})
