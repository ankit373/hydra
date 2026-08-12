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

/** One (source, domain) row of the calibration leaderboard — mirrors Go's
 * desktop/api.CalibrationRow, itself a mapping of internal/trust.Stat. */
export interface CalibrationRow {
  source: string
  domain: string
  d: number
  se: number
  sp: number
  n: number
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
  /** Never null — an empty leaderboard is a real, renderable state. */
  calibration: CalibrationRow[]
}

export interface Version {
  version: string
  commit: string
  date: string
}

export interface UpdateStatus {
  current: string
  /** Empty unless available is true. */
  latest?: string
  available: boolean
}

export interface UpgradeResult {
  ok: boolean
  /** Combined stdout+stderr of install-app.sh, for troubleshooting a failure. */
  output: string
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

export interface TimelineEntry {
  kind: string
  /** Empty when the event carried no parseable timestamp. */
  ts: string
  nodeId?: string
  head?: string
  model?: string
  tier: number
  status?: string
  costUsd: number
  durationMs: number
  confidence: number
  /** The verifiable part — for an SPRT sample, "agreed · LLR +1.2 → Λ 1.2". */
  detail?: string
}

/** An A2A collaboration edge — distinct from Agent.parent, which is ownership. */
export interface Edge {
  from: string
  to: string
  ts: string
  detail?: string
}

export interface Session {
  runId: string
  live: boolean
  /** False when the run id names no log — different from a run that did nothing. */
  found: boolean
  error?: string
  timeline: TimelineEntry[]
  agents: Agent[]
  edges: Edge[]
  /** True only when a list cannot convey the shape: a fan-out or an A2A edge. */
  nonLinear: boolean
  skipped: number
}

export interface Edit {
  file: string
  ts: string
  detail?: string
  /** Empty when no snapshot was stored — the change happened, the diff cannot be shown. */
  ref?: string
  added: number
  removed: number
}

export interface DiffLine {
  op: string
  text: string
  /** 0 when the line is an addition. */
  oldLine: number
  /** 0 when the line is a removal. */
  newLine: number
}

export interface Diff {
  file: string
  found: boolean
  /** Why a diff is unavailable, so the view is specific rather than blank. */
  reason?: string
  lines: DiffLine[]
  added: number
  removed: number
}

export interface HyctlStatus {
  found: boolean
  path?: string
  version?: string
  /** False on platforms InstallHyctl cannot drive (Windows) — see its Go doc. */
  supported: boolean
}

export interface InstallResult {
  ok: boolean
  version?: string
  /** The installer's combined stdout/stderr, shown on both success and failure. */
  log: string
  error?: string
}

export interface ChatReply {
  output: string
  head: string
  model: string
  tier: number
  costUsd: number
  durationMs: number
  /** Links the reply into Session — "why did it say that" is one click. */
  runId: string
  error?: string
  /** No heads discoverable at all — the dock offers to retry instead of
   *  showing a raw dispatch error. */
  needsProbe?: boolean
}
