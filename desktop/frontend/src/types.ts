// Hand-written mirrors of desktop/api's DTOs.
//
// Wails generates bindings into frontend/wailsjs at build time, but those are
// build artefacts and are gitignored, so the source tree cannot typecheck
// against them. These declarations let `npm run typecheck` run standalone; the
// generated bindings are structurally identical at runtime.

export interface SpendPanel {
  todayUsd: number
  allTimeUsd: number
  todayCalls: number
  totalCalls: number
  tokensActualPct: number
}

export interface GovernorPanel {
  known: boolean
  pct: number
  mode: string
}

export interface TrustPanel {
  runs: number
  meanSamples: number
  fixedSwarmN: number
  samplesSavedPct: number
  autoClearedPct: number
  meanTargetConf: number
  meanFinalConf: number
  totalCostUsd: number
}

export interface Breakdown {
  key: string
  calls: number
  promptTokens: number
  responseTokens: number
  costUsd: number
  wallMs: number
}

export interface RecentCall {
  ts: string
  model: string
  tier: number
  costUsd: number
  wallMs: number
  runId: string
  taskId: string
}

export interface Dashboard {
  hasData: boolean
  spend: SpendPanel
  governor: GovernorPanel
  trust: TrustPanel
  /** null — not [] — when nothing has ever dispatched. */
  byModel: Breakdown[] | null
  byTier: Breakdown[] | null
  byDay: Breakdown[] | null
  recent: RecentCall[] | null
}

export interface Version {
  version: string
  commit: string
  date: string
}

export interface Agent {
  id: string
  parent?: string
  depth: number
  head?: string
  model?: string
  tier: number
  state: string
  costUsd: number
  confidence: number
  durationMs: number
  detail?: string
}

export interface Run {
  id: string
  live: boolean
  startedAt: string
  elapsedMs: number
  costUsd: number
  /** Zero means "not a confidence run", not "no confidence". */
  confidence: number
  agents: Agent[]
  running: number
  ok: number
  failed: number
  pending: number
  allCount: number
  /** Events the reconstruction could not attribute — surfaced, never hidden. */
  skipped: number
  /** Set when this run's log could not be read; the row still renders. */
  error?: string
}

export interface Fleet {
  hasRuns: boolean
  liveCount: number
  runs: Run[]
  /** Agent count past which a run collapses. Defined in Go, not the view. */
  groupThreshold: number
}
