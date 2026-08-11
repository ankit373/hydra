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

export type CoverageStatus = 'enforced' | 'configured' | 'gap' | 'n/a'

export interface Category {
  id: string
  name: string
  status: CoverageStatus
  detail: string
  /** Set only when status is 'gap' — the earliest recorded run where this
   *  category was already a gap, from persisted score history. */
  gapSince?: string
  gapAgeDays?: number
}

export interface Coverage {
  categories: Category[]
  /** Categories excluding n/a. */
  applicable: number
  /** Enforced + Configured. */
  covered: number
  percentCovered: number
}

export interface Trend {
  available: boolean
  deltaPct: number
  firstPct: number
  firstTs: string
}

/** One persisted coverage snapshot — the real series a chart is drawn from. */
export interface HistoryPoint {
  ts: string
  percentCovered: number
}

export type ActionPriority = 'now' | 'soon' | 'watch'

/** One item in the prioritized action queue — the feedback loop. Priority
 *  comes from real signals (a gap's persisted age, or an actively risky
 *  head), never an invented severity score. */
export interface Action {
  id: string
  kind: 'gap' | 'risk'
  title: string
  detail: string
  ageDays: number
  priority: ActionPriority
}

export interface LedgerPanel {
  total: number
  allowed: number
  denied: number
  flagged: number
}

export interface HeadRisk {
  head: string
  denied: number
  flagged: number
}

export interface Check {
  name: string
  status: string
  detail: string
}

export interface SecurityReport {
  /** False when the ledger has never recorded an event. */
  hasData: boolean
  ledger: LedgerPanel
  byHead: HeadRisk[]
  checks: Check[]
  coverage: Coverage
  /** Hard override: false means the ledger chain was tampered with — the
   *  coverage percentage above cannot be trusted regardless of its value. */
  integrityIntact: boolean
  trend: Trend
  /** The full persisted coverage series, oldest first, this run last. */
  history?: HistoryPoint[]
  /** The feedback loop: one item per coverage gap plus one per risky head,
   *  ranked most-urgent first. */
  actions?: Action[]
  /** Denied/flagged bucketed by day — the "blocked over time" bypass-attempt
   *  trend, from ledger.ByDayRisk. */
  riskHistory?: DayRisk[]
  /** Per-rule hit counts, dead/unreachable rules, and the fail-open posture. */
  policyAudit: PolicyAudit
  /** Every sensitive-data detection and whether it left the machine. */
  exposures?: Exposure[]
  /** The forensic breakdown behind the blocked/flagged counts. */
  threats: Threats
  /** A capped tail of the raw ledger — the evidence rows. */
  events?: LedgerEvent[]
  /** True when `events` is a partial log, not the whole one. */
  truncated?: boolean
  /** Whether each declared control actually runs. */
  controls?: Control[]
}

export interface DayRisk {
  date: string
  denied: number
  flagged: number
}

export interface RuleStat {
  index: number
  summary: string
  decision: string
  hits: number
  /** Never matched anything recorded. */
  dead: boolean
  /** Index of an earlier rule that always matches first, so this one can
   *  never fire. Absent when the rule is reachable. */
  shadowedBy?: number
}

export interface PolicyAudit {
  rules: RuleStat[]
  default: string
  /** The default is allow: anything no rule names is permitted. */
  failOpen: boolean
  defaultHits: number
  evaluated: number
}

export interface Exposure {
  ts: string
  agent: string
  head: string
  resource: string
  /** The specific detectors that matched, e.g. "aws access key id". */
  piiTypes?: string[]
  /** Not a local-only head — including an undiscovered one (fail-closed). */
  remote: boolean
  /** False when the head was not discovered, so `remote` is an assumption
   *  rather than an observation. */
  known: boolean
}

/** One security control and whether it can actually fire. */
export interface Control {
  name: string
  /** Configured, or its code exists. */
  declared: boolean
  /** Can actually take effect at runtime. */
  wired: boolean
  /** Fires, but is weaker than it appears. */
  limited?: boolean
  detail: string
  /** False when the claim was established by reading the source rather than
   *  observed at runtime — the reader deserves to know which kind it is. */
  verified: boolean
}

export interface SecurityCount {
  label: string
  count: number
}

export interface Threats {
  /** Which injection phrase was actually tried. */
  byMarker?: SecurityCount[]
  /** Resources drawing repeat denials — probing. */
  probedResources?: SecurityCount[]
  /** read / write / exec / network split of risky events. */
  byAction?: SecurityCount[]
}

/** One raw ledger row — the evidence behind a finding. */
export interface LedgerEvent {
  ts: string
  agent: string
  tool: string
  resource: string
  action: string
  decision: string
  reason?: string
  classification?: string
  pii_types?: string[]
  flagged?: boolean
  flag_reason?: string
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
}
