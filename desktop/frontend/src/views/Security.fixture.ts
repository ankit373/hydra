import type {
  Attestation,
  Check,
  PolicyAudit,
  Posture,
  SecurityReport,
} from '../types'

/**
 * A complete SecurityReport, overridable a field at a time.
 *
 * This is the deliverable as much as the tests are. The Audit view is the
 * largest in the app and had no tests at all, purely because its only prop is
 * a report with fifteen required nested fields and no fixture for one existed
 * anywhere, not in the frontend, not on the Go side. Every conditional path
 * in the view was therefore unreachable from a test, which is how two hero
 * cards came to look checks up by names the backend never emits.
 *
 * Defaults describe a quiet, healthy machine, so a test states only the
 * condition it is about.
 */
export function securityReport(over: Partial<SecurityReport> = {}): SecurityReport {
  return {
    hasData: true,
    integrityIntact: true,
    ledger: { total: 42, allowed: 40, denied: 2, flagged: 0 },
    byHead: [],
    checks: [],
    coverage: { categories: [], applicable: 10, covered: 8, percentCovered: 80 },
    trend: { available: true, deltaPct: 4, firstPct: 76, firstTs: '2026-08-01T00:00:00Z' },
    policyAudit: { rules: [], default: 'deny', failOpen: false, defaultHits: 0, evaluated: 42 },
    threats: {},
    evidence: { runs: 12 },
    drift: { changed: false, unstamped: 0 },
    supplyChain: { new: 0, changed: 0 },
    blast: { graphPresent: true, percolates: false, unknown: 0, runsScanned: 5 },
    posture: posture(),
    register: { risks: [], sumDefectCostUsd: 0, breached: 0, bySeverity: {} },
    attestation: attestation(),
    ...over,
  }
}

export function posture(over: Partial<Posture> = {}): Posture {
  return {
    verdict: 'ok',
    trigger: 'no condition met',
    checked: ['chain integrity', 'policy posture'],
    ...over,
  }
}

export function attestation(over: Partial<Attestation> = {}): Attestation {
  return {
    generatedAt: '2026-09-04T00:00:00Z',
    tool: 'hyctl',
    version: '1.3.1',
    evidence: { events: 42, chainedEvents: 42, chainIntact: true },
    verdict: 'ok',
    trigger: 'no condition met',
    coveragePercent: 80,
    openRisks: 0,
    bySeverity: { critical: 0, high: 0, medium: 0, low: 0 },
    slaBreached: 0,
    incidents: 0,
    digest: 'sha256:0000',
    ...over,
  }
}

export function policyAudit(over: Partial<PolicyAudit> = {}): PolicyAudit {
  return { rules: [], default: 'deny', failOpen: false, defaultHits: 0, evaluated: 42, ...over }
}

/** A named check, since the view finds these by exact name. */
export function check(name: string, status: string, detail = 'd'): Check {
  return { name, status, detail }
}
