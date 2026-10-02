import './__mock'
import { securityReport } from './src/views/Security.fixture'
;(window as any).__SEC = () =>
  Promise.resolve(
    securityReport({
      checks: [
        { name: 'local-only enforcement', status: 'pass', detail: 'PII routes to local heads only' },
        { name: 'ledger chain', status: 'pass', detail: '42 events chained, anchor present' },
        { name: 'state dir permissions', status: 'warn', detail: '~/.hydra is 0755; other users can read the baseline' },
      ],
      coverage: {
        edition: '2025', applicable: 10, covered: 7, partial: 2, percentCovered: 70,
        categories: [
          { id: 'LLM01', name: 'Prompt Injection', status: 'enforced', detail: 'egress gate + marker detection' },
          { id: 'LLM02', name: 'Sensitive Information Disclosure', status: 'enforced', detail: 'PII detector, local-only routing' },
          { id: 'LLM03', name: 'Supply Chain', status: 'configured', detail: 'head binaries fingerprinted' },
          { id: 'LLM04', name: 'Data and Model Poisoning', status: 'gap', detail: 'no corpus provenance', gapSince: '2026-07-04', gapAgeDays: 86 },
          { id: 'LLM05', name: 'Improper Output Handling', status: 'partial', detail: 'detective only' },
          { id: 'LLM06', name: 'Excessive Agency', status: 'enforced', detail: 'ledger policy gate' },
        ],
      },
      register: {
        sumDefectCostUsd: 42000, breached: 1, bySeverity: { critical: 0, high: 1, medium: 2, low: 3 },
        risks: [
          { id: 'R-1', class: 'coverage', title: 'No corpus provenance', detail: 'LLM04 has been a gap for 86 days', severity: 'high', status: 'open', ageDays: 86, dueInDays: -26, breached: true, defectCostUsd: 25000, frameworks: [{ framework: 'OWASP', control: 'LLM04', curated: true }] },
          { id: 'R-2', class: 'supply-chain', title: 'Head binary changed', detail: 'cli/codex sha256 differs from the stored fingerprint', severity: 'medium', status: 'open', ageDays: 3, dueInDays: 11, breached: false, defectCostUsd: 12000 },
          { id: 'R-3', class: 'policy', title: 'Dead rule', detail: 'rule 4 is shadowed by rule 2 and can never fire', severity: 'low', status: 'open', ageDays: 12, dueInDays: 48, breached: false, defectCostUsd: 5000 },
        ],
      },
      actions: [
        { id: 'A-1', kind: 'gap', title: 'Close LLM04: data and model poisoning', detail: 'Record corpus provenance on every evalset example.', ageDays: 86, priority: 'now' },
        { id: 'A-2', kind: 'risk', title: 'Re-fingerprint cli/codex', detail: 'The binary changed since it was last seen.', ageDays: 3, priority: 'soon' },
      ],
      boundary: { governed: ['anthropic/claude-sonnet-4.5', 'openai/gpt-5', 'ollama/qwen3:8b'], opaque: ['cli/claude', 'agy/claude-opus'], opaqueLocalOnly: [] },
      history: Array.from({ length: 10 }, (_, i) => ({ ts: new Date(Date.now() - (9 - i) * 864e5).toISOString(), percentCovered: 62 + i * 1.2 })),
      riskHistory: Array.from({ length: 10 }, (_, i) => ({ date: new Date(Date.now() - (9 - i) * 864e5).toISOString().slice(0, 10), denied: i % 4, flagged: (i + 1) % 3 })),
    }),
  )
import('./src/main')
