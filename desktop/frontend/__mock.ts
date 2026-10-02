// Dev-only harness: stands in for the Wails bridge so the UI can be rendered
// and screenshotted in a browser. Not shipped, not imported by src/.
const delay = <T,>(v: T) => new Promise<T>((r) => setTimeout(() => r(v), 60))

const bd = (key: string, calls: number, cost: number) => ({
  key, calls, promptTokens: calls * 1800, responseTokens: calls * 640,
  costUsd: cost, wallMs: calls * 2400,
})

const agents = (n: number, head: string) =>
  Array.from({ length: n }, (_, i) => ({
    id: `a${i}`, parent: i === 0 ? undefined : 'a0', depth: i === 0 ? 0 : 1,
    head, model: head, tier: 4 + (i % 3), state: i === 0 ? 'ok' : i === 1 ? 'running' : 'ok',
    costUsd: 0.004 * (i + 1), confidence: 0.82 + i * 0.03, durationMs: 1200 + i * 800,
    detail: i === 0 ? 'accepted' : 'agreed · LLR +1.2 → Λ 1.2',
  }))

const run = (id: string, live: boolean, goal: string, cost: number) => ({
  id, live, waiting: false, startedAt: new Date(Date.now() - 6e5).toISOString(),
  elapsedMs: 42_000, costUsd: cost, confidence: 0.94, agents: agents(4, 'anthropic/claude-sonnet-4.5'),
  running: live ? 1 : 0, ok: 3, failed: 0, pending: 0, allCount: 4, skipped: 0, goal,
})

const API = {
  GetDashboard: () => delay({
    hasData: true,
    spend: { todayUsd: 1.8342, allTimeUsd: 64.2119, todayCalls: 87, totalCalls: 3128, tokensActualPct: 71 },
    governor: { known: true, pct: 52, mode: 'compact', effectiveMode: 'compact', burnRatePct: 1.4, risk: 0.18, observations: 24, horizonObs: 20 },
    trust: { runs: 61, meanSamples: 2.4, fixedSwarmN: 5, samplesSavedPct: 52, autoClearedPct: 78, meanTargetConf: 0.9, meanFinalConf: 0.94, totalCostUsd: 12.44 },
    byModel: [bd('anthropic/claude-sonnet-4.5', 402, 18.22), bd('openai/gpt-5', 231, 12.07), bd('google/gemini-2.5-pro', 188, 6.41), bd('ollama/qwen3:8b', 1904, 0), bd('agy/claude-opus', 96, 21.3)],
    byTier: [bd('1', 96, 21.3), bd('4', 402, 18.22), bd('8', 431, 8.9), bd('10', 1904, 0)],
    byDay: Array.from({ length: 14 }, (_, i) => bd(new Date(Date.now() - (13 - i) * 864e5).toISOString().slice(0, 10), 40 + i * 9, 1.2 + i * 0.4)),
    recent: Array.from({ length: 8 }, (_, i) => ({ ts: new Date(Date.now() - i * 42e4).toISOString(), model: ['anthropic/claude-sonnet-4.5', 'ollama/qwen3:8b', 'openai/gpt-5'][i % 3], tier: [4, 10, 2][i % 3], costUsd: [0.021, 0, 0.038][i % 3], wallMs: 1800 + i * 300, runId: `run-${100 + i}`, taskId: `task-${100 + i}` })),
    calibration: [
      { source: 'anthropic/claude-sonnet-4.5', domain: 'go', d: 1.84, se: 0.91, sp: 0.74, n: 142 },
      { source: 'ollama/qwen3:8b', domain: 'go', d: 0.62, se: 0.71, sp: 0.55, n: 310 },
      { source: 'openai/gpt-5', domain: 'typescript', d: 1.41, se: 0.88, sp: 0.69, n: 88 },
      { source: 'google/gemini-2.5-pro', domain: 'go', d: 1.12, se: 0.8, sp: 0.66, n: 54 },
    ],
  }),
  GetFleet: () => delay({ hasRuns: true, liveCount: 2, waitingCount: 1, groupThreshold: 8, runs: [run('run-108', true, 'rotate the signing key in internal/auth/token.go', 0.062), run('run-107', true, 'add pagination to the heads endpoint', 0.019), run('run-106', false, 'fix the flaky workflow resume test', 0.204), run('run-105', false, 'review the diff on feature/905', 0.088)] }),
  GetSession: (id: string) => delay({ runId: id || 'run-108', live: true, found: true, goal: 'rotate the signing key in internal/auth/token.go', nonLinear: true, skipped: 0, agents: agents(5, 'anthropic/claude-sonnet-4.5'), edges: [{ from: 'a0', to: 'a1', ts: new Date().toISOString(), detail: 'handoff' }], timeline: Array.from({ length: 7 }, (_, i) => ({ kind: ['task_started', 'head_selected', 'sprt_sample', 'sprt_sample', 'edit_applied', 'verified', 'task_done'][i], ts: new Date(Date.now() - (7 - i) * 9e3).toISOString(), nodeId: `a${i % 4}`, head: 'anthropic/claude-sonnet-4.5', model: 'claude-sonnet-4.5', tier: 4, status: 'ok', costUsd: 0.008 * i, durationMs: 900 * i, confidence: 0.6 + i * 0.05, detail: 'agreed · LLR +1.2 → Λ 2.4' })) }),
  GetEdits: () => delay([{ file: 'internal/auth/token.go', ts: new Date().toISOString(), detail: 'rotate signing key', ref: 'abc123', added: 24, removed: 9 }, { file: 'internal/auth/token_test.go', ts: new Date().toISOString(), detail: 'cover the rotation', ref: 'abc124', added: 41, removed: 0 }]),
  GetDiff: () => delay({ file: 'internal/auth/token.go', found: true, added: 3, removed: 1, lines: [{ op: ' ', text: 'func Rotate(k Key) error {', oldLine: 10, newLine: 10 }, { op: '-', text: '\treturn nil', oldLine: 11, newLine: 0 }, { op: '+', text: '\tif err := k.Validate(); err != nil {', oldLine: 0, newLine: 11 }, { op: '+', text: '\t\treturn err', oldLine: 0, newLine: 12 }, { op: '+', text: '\t}', oldLine: 0, newLine: 13 }, { op: ' ', text: '}', oldLine: 12, newLine: 14 }] }),
  Chat: () => delay({ output: 'Routed to anthropic/claude-sonnet-4.5 (tier 4).\n\nThe signing key rotation needs the validator to run before the swap, otherwise a malformed key replaces a working one.', head: 'anthropic/claude-sonnet-4.5', model: 'claude-sonnet-4.5', tier: 4, costUsd: 0.0182, durationMs: 2400, runId: 'run-109' }),
  ApproveEdit: (f: string) => delay({ file: f, status: 'approved' }),
  RejectEdit: (f: string) => delay({ file: f, status: 'reverted', method: 'git_checkout' }),
  GetMCPServers: () => delay({ scanned: true, synced: new Date().toISOString(), servers: [{ name: 'filesystem', client: 'claude-code', scope: 'user', package: '@modelcontextprotocol/server-filesystem', remote: false, status: 'verified', state: 'trusted', scored: true, score: 82, confidence: 'high' }, { name: 'postmark-mcp', client: 'claude-code', scope: 'project', package: 'postmark-mcp', remote: false, status: 'verified', state: 'quarantined', scored: true, score: -80, confidence: 'high', nearestMatch: 'postmark', nearestDist: 4 }, { name: 'grafana', client: 'claude-code', scope: 'user', remote: true, status: 'unresolved', state: 'provisional', scored: false, score: 0 }] }),
  SyncMCPRegistry: () => delay({ servers: 412 }),
  GetHeads: () => delay({
    routable: 2,
    heads: [
      { id: 'claude-sonnet-4.5', name: 'Claude Sonnet 4.5', provider: 'anthropic', source: 'env', tier: 4, capScore: 92, routable: true, localOnly: false },
      { id: 'qwen3:8b', name: 'Qwen3 8B', provider: 'ollama', source: 'port', tier: 10, capScore: 68, routable: true, localOnly: true },
      { id: 'claude-opus-4.6', name: 'Claude Opus 4.6', provider: 'anthropic', source: 'env', tier: 1, capScore: 97, routable: false, localOnly: false, reason: 'no ANTHROPIC_API_KEY in the environment' },
    ],
  }),
  GetPendingQuestions: () => delay({ questions: [{ taskId: 'task-441', runId: 'run-106', question: 'Write to internal/auth/token.go outside the allowed scope?', head: 'anthropic/claude-sonnet-4.5', resource: 'internal/auth/token.go', prompt: 'rotate the signing key', askedAtMs: Date.now() - 42e4 }] }),
  AnswerQuestion: () => delay({ output: 'ok', head: 'x', model: 'x', tier: 4, costUsd: 0, durationMs: 0, runId: 'run-106' }),
  DeclineQuestion: () => delay(undefined),
  ChatEnums: () => delay(['SIMPLE', 'MODERATE', 'STANDARD', 'COMPLEX', 'EXPERT']),
  NewRunID: () => delay('run-' + Math.random().toString(16).slice(2, 8)),
  GetVersion: () => delay({ version: 'v1.5.0', commit: '619cdad', date: '2026-09-28' }),
  GetUpdateStatus: () => delay({ current: 'v1.5.0', available: false }),
  TriggerUpgrade: () => delay({ ok: true, output: '' }),
  CheckHyctl: () => delay({ found: true, path: '/usr/local/bin/hyctl', version: 'v1.5.0', supported: true }),
  InstallHyctl: () => delay({ ok: true, version: 'v1.5.0', log: '' }),
  GetModels: () => delay({ found: true, pools: [
    { name: 'anthropic', shared: true, observedCalls: 402, observedCostUsd: 18.22, observedTokens: 1_240_000, models: [
      { id: 'claude-opus-4.6', name: 'Claude Opus 4.6', tier: 1, provider: 'anthropic', pool: 'anthropic', complexityMin: 85, complexityMax: 100, speed: 'slow', accuracy: 'very_high', contextWindow: 200000, enabled: true },
      { id: 'claude-sonnet-4.5', name: 'Claude Sonnet 4.5', tier: 4, provider: 'anthropic', pool: 'anthropic', complexityMin: 60, complexityMax: 85, speed: 'medium', accuracy: 'high', contextWindow: 200000, enabled: true }] },
    { name: 'local', shared: false, observedCalls: 1904, observedCostUsd: 0, observedTokens: 4_100_000, models: [
      { id: 'qwen3:8b', name: 'Qwen3 8B', tier: 10, provider: 'ollama', pool: 'local', complexityMin: 0, complexityMax: 55, speed: 'fast', accuracy: 'medium', contextWindow: 32768, enabled: true }] }] }),
  GetSettings: () => delay((window as any).__SET ?? ((window as any).__SET = {
    path: '/Users/a/.hydra/config.toml', readable: true, exists: true,
    cortex: 'anthropic/claude-sonnet-4.5', skills: ['code-gen', 'review'],
    piiLocalOnly: true, strictEgress: true,
    capturePayloads: false, payloadBudgetMb: 0,
    captureEmbeddings: true, embedModel: '', embedBudgetMb: 256,
    cacheAnswers: true, cacheThreshold: 0.95, cacheBudgetMb: 128,
    exploreRate: 0.05,
    openRouterModels: ['anthropic/claude-sonnet-4.5', 'google/gemini-2.5-pro'],
  })),
  SaveSettings: (s: any) => delay(((window as any).__SET = { ...s, error: '' })),
  GetSecurity: () => (window as any).__SEC(),
}

// Three machines, not one. The healthy mock above is the case the design was
// drawn against; a first run and a backend that will not answer are the two
// the app had never been rendered in. `?state=fresh` / `?state=broken`.
const state = new URLSearchParams(location.search).get('state') ?? 'healthy'

const nothing = {
  GetDashboard: () => delay({
    hasData: false,
    spend: { todayUsd: 0, allTimeUsd: 0, todayCalls: 0, totalCalls: 0, tokensActualPct: 0 },
    governor: { known: false, pct: 0, mode: '', effectiveMode: '', burnRatePct: 0, risk: 0, observations: 0, horizonObs: 0 },
    trust: { runs: 0, meanSamples: 0, fixedSwarmN: 0, samplesSavedPct: 0, autoClearedPct: 0, meanTargetConf: 0, meanFinalConf: 0, totalCostUsd: 0 },
    byModel: [], byTier: [], byDay: [], recent: [], calibration: [],
  }),
  GetFleet: () => delay({ hasRuns: false, liveCount: 0, waitingCount: 0, groupThreshold: 8, runs: [] }),
  GetSession: () => delay({ runId: '', live: false, found: false, goal: '', nonLinear: false, skipped: 0, agents: [], edges: [], timeline: [] }),
  GetEdits: () => delay([]),
  GetHeads: () => delay({ routable: 0, heads: [] }),
  GetMCPServers: () => delay({ scanned: false, synced: '', servers: [] }),
  GetPendingQuestions: () => delay({ questions: [] }),
  CheckHyctl: () => delay({ found: false, path: '', version: '', supported: false }),
  GetSettings: () => delay({
    path: '/Users/a/.hydra/config.toml', readable: true, exists: false,
    cortex: '', skills: [], piiLocalOnly: false, strictEgress: true,
    capturePayloads: false, payloadBudgetMb: 0, captureEmbeddings: false,
    embedModel: '', embedBudgetMb: 0, cacheAnswers: false, cacheThreshold: 0,
    cacheBudgetMb: 0, exploreRate: 0, openRouterModels: [],
  }),
  GetModels: () => delay({ found: true, pools: [
    { name: 'anthropic', shared: true, observedCalls: 0, observedCostUsd: 0, observedTokens: 0, models: [
      { id: 'claude-sonnet-4.5', name: 'Claude Sonnet 4.5', tier: 4, provider: 'anthropic', pool: 'anthropic', complexityMin: 60, complexityMax: 85, speed: 'medium', accuracy: 'high', contextWindow: 200000, enabled: true }] }] }),
}

const refused = (what: string) => () => Promise.reject(new Error(what))
const broken = {
  GetDashboard: refused('read /Users/a/.hydra/logs/cost.jsonl: input/output error'),
  GetFleet: refused('read /Users/a/.hydra/logs/runs: input/output error'),
  GetSession: refused('read /Users/a/.hydra/logs/runs/run-108.jsonl: input/output error'),
  GetEdits: refused('read /Users/a/.hydra/logs/last_edit.json: input/output error'),
  GetHeads: refused('probe: dial tcp 127.0.0.1:11434: connect: connection refused'),
  GetModels: refused('registry/models.yaml: yaml: line 42: mapping values are not allowed in this context'),
  GetMCPServers: refused('open /Users/a/.hydra/mcp_ledger.jsonl: permission denied'),
  GetSecurity: refused('open /Users/a/.hydra/head_binaries.json: permission denied'),
  GetPendingQuestions: refused('read /Users/a/.hydra/logs/pending: input/output error'),
  GetVersion: refused('exec: "hyctl": executable file not found in $PATH'),
  GetSettings: () => delay({
    path: '/Users/a/.hydra/config.toml', readable: false, exists: true,
    error: 'config /Users/a/.hydra/config.toml is not readable: toml: line 5 (last key "cache_threshold"): expected value but found "oops" instead',
    cortex: '', skills: [], piiLocalOnly: false, strictEgress: true,
    capturePayloads: false, payloadBudgetMb: 0, captureEmbeddings: false,
    embedModel: '', embedBudgetMb: 0, cacheAnswers: false, cacheThreshold: 0,
    cacheBudgetMb: 0, exploreRate: 0, openRouterModels: [],
  }),
  CheckHyctl: () => delay({ found: false, path: '', version: '', supported: false }),
}

const overlay = state === 'fresh' ? nothing : state === 'broken' ? broken : {}
;(window as any).go = { api: { API: { ...API, ...overlay } } }
;(window as any).runtime = { EventsOn: () => () => {}, EventsOff: () => {}, EventsEmit: () => {} }
export {}
