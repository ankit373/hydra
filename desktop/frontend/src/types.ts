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
