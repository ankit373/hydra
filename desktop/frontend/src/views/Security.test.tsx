import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { Security } from './Security'
import { attestation, check, policyAudit, posture, securityReport } from './Security.fixture'

afterEach(cleanup)

describe('the verdict and the measurement', () => {
  it('leads with the verdict rather than a table', () => {
    render(<Security data={securityReport({ posture: posture({ verdict: 'act now', trigger: 'chain broken' }) })} />)
    expect(screen.getByText(/act now/i)).toBeInTheDocument()
    expect(screen.getByText(/chain broken/)).toBeInTheDocument()
  })

  // LedgerCard lives under a detail tab, so this is only reachable there,
  // which is part of why the state had never been exercised.
  it('says so when the audit log is empty rather than rendering zeroes as fact', () => {
    render(<Security data={securityReport({ hasData: false })} />)
    fireEvent.click(screen.getByRole('button', { name: 'Detailed' }))
    fireEvent.click(screen.getByRole('button', { name: 'Evidence' }))
    expect(screen.getByText(/audit log is empty/i)).toBeInTheDocument()
  })
})

// The two cards that had never rendered: both looked checks up by names
// internal/security does not emit, and findCheckStatus compares exactly.
describe('the hero cards that read from named checks', () => {
  it('shows guardrails from the Policy posture check', () => {
    render(
      <Security
        data={securityReport({ checks: [check('Policy posture', 'all rules scoped')] })}
      />,
    )
    expect(screen.getByText('Guardrails')).toBeInTheDocument()
    expect(screen.getByText('all rules scoped')).toBeInTheDocument()
  })

  it('shows sensitive data from the Sensitive data exposure check', () => {
    render(
      <Security
        data={securityReport({ checks: [check('Sensitive data exposure', 'none detected')] })}
      />,
    )
    expect(screen.getByText('none detected')).toBeInTheDocument()
  })

  // The label is UI vocabulary; the name is a data key. Renaming one must not
  // move the other, which is exactly how the cards broke.
  it('does not render the card when the check is absent', () => {
    render(<Security data={securityReport({ checks: [] })} />)
    expect(screen.queryByText('Guardrails')).not.toBeInTheDocument()
  })
})

describe('the banners, which are conditional and so were never exercised', () => {
  it('overrides everything when the chain was tampered with', () => {
    render(<Security data={securityReport({ integrityIntact: false })} />)
    const banner = screen.getByText(/INTEGRITY COMPROMISED/)
    expect(banner).toBeInTheDocument()
    // It names the audit log, not the internal structure.
    expect(banner.textContent).toMatch(/audit log's hash chain/i)
    expect(banner.textContent).not.toMatch(/ledger/i)
  })

  it('is silent about integrity when the chain is intact', () => {
    render(<Security data={securityReport()} />)
    expect(screen.queryByText(/INTEGRITY COMPROMISED/)).not.toBeInTheDocument()
  })

  // The banner exists to give one instruction, and it named no file, so the
  // instruction could not be followed.
  it('names the file to edit when the policy is fail-open', () => {
    render(<Security data={securityReport({ policyAudit: policyAudit({ failOpen: true }) })} />)
    fireEvent.click(screen.getByRole('button', { name: 'Detailed' }))
    fireEvent.click(screen.getByRole('button', { name: 'Guardrails' }))
    const banner = screen.getByText(/FAIL-OPEN/)
    expect(banner.textContent).toMatch(/~\/\.hydra\/mcp_policy\.json/)
  })

  it('shows no fail-open banner when the default is deny', () => {
    render(<Security data={securityReport()} />)
    fireEvent.click(screen.getByRole('button', { name: 'Detailed' }))
    fireEvent.click(screen.getByRole('button', { name: 'Guardrails' }))
    expect(screen.queryByText(/FAIL-OPEN/)).not.toBeInTheDocument()
  })
})

describe('the tabs', () => {
  it('starts on Overview and switches to Detailed', () => {
    render(<Security data={securityReport()} />)
    const overview = screen.getByRole('button', { name: 'Overview' })
    expect(overview).toHaveAttribute('aria-current', 'page')

    fireEvent.click(screen.getByRole('button', { name: 'Detailed' }))
    expect(screen.getByRole('button', { name: 'Detailed' })).toHaveAttribute('aria-current', 'page')
  })

  // The point is that none of these throw on a report with empty collections,
  // which is the state a fresh machine is actually in.
  it('renders every detail tab without throwing', () => {
    render(<Security data={securityReport()} />)
    fireEvent.click(screen.getByRole('button', { name: 'Detailed' }))

    for (const tab of [
      'Register', 'Coverage', 'Controls', 'Guardrails', 'Exposure',
      'Threats', 'Access', 'Estate', 'Evidence', 'Attestation',
    ]) {
      const btn = screen.queryByRole('button', { name: tab })
      if (!btn) continue
      expect(() => fireEvent.click(btn)).not.toThrow()
    }
  })

  it('uses the UI vocabulary for the guardrails tab, not the internal name', () => {
    render(<Security data={securityReport()} />)
    fireEvent.click(screen.getByRole('button', { name: 'Detailed' }))
    expect(screen.getByRole('button', { name: 'Guardrails' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Policy' })).not.toBeInTheDocument()
  })
})

describe('the evidence tail', () => {
  it('says the shown events are a partial log when they are', () => {
    render(
      <Security
        data={securityReport({
          truncated: true,
          events: [{ ts: '2026-09-04T00:00:00Z', agent: 'a', tool: 't', resource: 'r', action: 'exec', decision: 'allow' }],
        })}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Detailed' }))
    fireEvent.click(screen.getByRole('button', { name: 'Evidence' }))
    expect(screen.getByText(/full audit log is longer/i)).toBeInTheDocument()
  })
})

describe('CSV export', () => {
  it('offers an export without throwing on an empty category list', () => {
    // jsdom has no real download; the click must still not throw.
    const url = URL.createObjectURL
    URL.createObjectURL = vi.fn(() => 'blob:x')
    URL.revokeObjectURL = vi.fn()
    try {
      render(<Security data={securityReport({ attestation: attestation() })} />)
      expect(() => fireEvent.click(screen.getByRole('button', { name: /export csv/i }))).not.toThrow()
    } finally {
      URL.createObjectURL = url
    }
  })
})
