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
  /** What the router acts on: the level band raised by rate-driven risk.
   *  Equals `mode` when there is no rate signal. */
  effectiveMode: string
  /** Mean percentage-point change per observation, and the probability of
   *  reaching 80% within `horizonObs`. Both zero when `observations` < 2,
   *  that is "no rate signal", not "no risk". */
  burnRatePct: number
  risk: number
  /** Counts of claude_pct *updates*, not wall-clock time, not chat turns. */
  observations: number
  horizonObs: number
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

/** One (source, domain) row of the calibration leaderboard, mirrors Go's
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
  /** null, not [], when nothing has ever dispatched. */
  byModel: Breakdown[] | null
  byTier: Breakdown[] | null
  byDay: Breakdown[] | null
  recent: RecentCall[] | null
  /** Never null, an empty leaderboard is a real, renderable state. */
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
  /** A question for this run is still parked. */
  waiting: boolean
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
  /** Events the reconstruction could not attribute, surfaced, never hidden. */
  skipped: number
  /** What was asked, in the requester's words. Empty when the run recorded no
   *  prompt; a preview, because that is what the log stores. */
  goal?: string
  /** Set when this run's log could not be read; the row still renders. */
  error?: string
}

export interface Fleet {
  hasRuns: boolean
  liveCount: number
  /** Runs parked on a question. The opposite of liveCount: stopped, and only
   *  a person can restart them. */
  waitingCount: number
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
  /** The verifiable part, for an SPRT sample, "agreed · LLR +1.2 → Λ 1.2". */
  detail?: string
}

/** An A2A collaboration edge, distinct from Agent.parent, which is ownership. */
export interface Edge {
  from: string
  to: string
  ts: string
  detail?: string
}

export interface Session {
  runId: string
  live: boolean
  /** False when the run id names no log, different from a run that did nothing. */
  found: boolean
  error?: string
  /** What this run was asked to do. Empty when it recorded no prompt. */
  goal?: string
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
  /** Empty when no snapshot was stored, the change happened, the diff cannot be shown. */
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
  /** Byte ranges on this line that actually changed, for a 1:1 replacement.
   *  Absent means no intra-line detail, the line was added or removed
   *  outright, or it is part of a block replacement where pairing would
   *  invent a relationship the diff never established. */
  spans?: Span[]
}

/** One MCP server installed on this machine. Identity only, by construction. */
export interface MCPServer {
  name: string
  client: string
  scope: string
  command?: string
  package?: string
  remote: boolean
  /** "verified" or "unresolved". With no sync, everything is unresolved. */
  status: string
  state?: string
  /** False when no score exists, so the view says "not scored" rather than 0. */
  scored: boolean
  score: number
  confidence?: string
  /** Closest known identifier and its edit distance, the typosquat signal. */
  nearestMatch?: string
  nearestDist?: number
}

/** What this machine can let an agent call, and what is known about it. */
export interface MCPPanel {
  servers: MCPServer[]
  /** When the official registry was last pulled; empty means never, and then
   *  every server reads unresolved for that reason alone. */
  synced?: string
  /** False when the audit could not run, so [] does not read as "none installed". */
  scanned: boolean
  error?: string
}

export interface MCPSyncResult {
  servers: number
  error?: string
}

/** One discovered head and whether anything can actually drive it. */
export interface Head {
  id: string
  name: string
  provider: string
  source: string
  tier: number
  capScore: number
  routable: boolean
  localOnly: boolean
  /** Why nothing can drive it, in words. Empty when routable. */
  reason?: string
}

/** What this machine can route to right now. */
export interface HeadPanel {
  heads: Head[]
  /** Counted in Go so a view need not re-derive it, and so "none discovered"
   *  is distinguishable from "discovered, none usable". */
  routable: number
}

/** The result of accepting or undoing one edit. */
export interface ReviewOutcome {
  file: string
  status?: string
  /** How the rollback was done, git_checkout, rm_untracked or
   *  backup_restore. Shown because the three are not equally recoverable. */
  method?: string
  error?: string
}

/** A byte range within DiffLine.text. */
export interface Span {
  start: number
  end: number
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
  /** Why version is empty despite hyctl being found, a timeout reads differently from no output. */
  versionError?: string
  /** False on platforms InstallHyctl cannot drive (Windows), see its Go doc. */
  supported: boolean
}

export interface InstallResult {
  ok: boolean
  version?: string
  /** Set when the post-install probe could not read a version; the install still succeeded. */
  versionError?: string
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
  /** Set only when status is 'gap', the earliest recorded run where this
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

/** One persisted coverage snapshot, the real series a chart is drawn from. */
export interface HistoryPoint {
  ts: string
  percentCovered: number
}

export type ActionPriority = 'now' | 'soon' | 'watch'

/** One item in the prioritized action queue, the feedback loop. Priority
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
  /** Hard override: false means the ledger chain was tampered with, the
   *  coverage percentage above cannot be trusted regardless of its value. */
  integrityIntact: boolean
  trend: Trend
  /** The full persisted coverage series, oldest first, this run last. */
  history?: HistoryPoint[]
  /** The feedback loop: one item per coverage gap plus one per risky head,
   *  ranked most-urgent first. */
  actions?: Action[]
  /** Denied/flagged bucketed by day, the "blocked over time" bypass-attempt
   *  trend, from ledger.ByDayRisk. */
  riskHistory?: DayRisk[]
  /** Per-rule hit counts, dead/unreachable rules, and the fail-open posture. */
  policyAudit: PolicyAudit
  /** Every sensitive-data detection and whether it left the machine. */
  exposures?: Exposure[]
  /** The forensic breakdown behind the blocked/flagged counts. */
  threats: Threats
  /** A capped tail of the raw ledger, the evidence rows. */
  events?: LedgerEvent[]
  /** True when `events` is a partial log, not the whole one. */
  truncated?: boolean
  /** Whether each declared control actually runs. */
  controls?: Control[]
  /** Whether the reported confidence rests on independent, discriminating sources. */
  evidence: EvidenceQuality
  /** Whether the ledger spans more than one configuration. */
  drift: ConfigDrift
  /** CLI head binaries and whether any changed since last seen. */
  supplyChain: SupplyChain
  /** Reach of what agents actually edited, per the code graph. */
  blast: BlastReport
  /** The one-line verdict and what decided it. */
  posture: Posture
  /** Correlated attack sequences. */
  incidents?: Incident[]
  /** The governed view: every finding as one kind of object. */
  register: RiskRegister
  /** Checkable, point-in-time statement of posture. Deliberately unsigned. */
  attestation: Attestation
  /** Entitlement review: what each agent touched vs what a rule scopes. */
  privilege?: AgentPrivilege[]
  /** AI bill of materials: the model estate, with provenance. */
  bom?: BOMEntry[]
}

/** What was true, under which rules, over which evidence. */
export interface Attestation {
  generatedAt: string
  tool: string
  version: string
  commit?: string
  /** Deployment breadcrumb: which routing/policy files were in force. */
  configFingerprint?: string
  evidence: AttestedEvidence
  verdict: Verdict
  trigger: string
  coveragePercent: number
  openRisks: number
  bySeverity: Record<Severity, number>
  slaBreached: number
  incidents: number
  /** Open risks per framework, so a reader can see it from their own standard. */
  frameworks?: Record<string, number>
  /** sha256 over every field above, so two copies can be compared. */
  digest: string
}

/** The state of the log the attestation rests on. */
export interface AttestedEvidence {
  events: number
  chainedEvents: number
  chainIntact: boolean
  /** Reported, never hidden: an attestation over an unverifiable log must say so. */
  truncated?: boolean
  anchorMissing?: boolean
}

/** One agent's observed footprint, for the least-privilege review. */
export interface AgentPrivilege {
  agent: string
  allowed: number
  denied: number
  resources?: string[]
  actions?: string[]
  heads?: string[]
  /** True when no policy rule names this agent, so it runs under the default. */
  unscoped: boolean
  writesOrExecs: number
}

/** One model in the estate. */
export interface BOMEntry {
  headId: string
  name?: string
  provider?: string
  /** How it was discovered: cli, env, port. */
  source?: string
  /** "builtin" (curated catalog) or "user" (added at runtime). */
  origin?: string
  local: boolean
  fingerprint?: string
  /** True when the ledger shows this head actually handling work. */
  used: boolean
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
  /** Not a local-only head, including an undiscovered one (fail-closed). */
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
   *  observed at runtime, the reader deserves to know which kind it is. */
  verified: boolean
}

export type Verdict = 'act now' | 'attention' | 'ok'
export type Severity = 'critical' | 'high' | 'medium' | 'low'

export interface Posture {
  verdict: Verdict
  /** The single condition that produced the verdict. */
  trigger: string
  because?: string[]
  /** What was evaluated, so an "ok" states its scope. */
  checked: string[]
}

export interface Incident {
  id: string
  actor: string
  agent?: string
  start: string
  end: string
  stages: string[]
  /** OWASP Risk Rating factors, kept separate so severity can be argued with. */
  likelihood: number
  impact: number
  severity: Severity
  narrative: string
  events: LedgerEvent[]
}

export interface FrameworkRef {
  framework: string
  control: string
  /** A hand-maintained mapping, not something derived from the data. */
  curated: boolean
}

export interface Risk {
  id: string
  class: string
  title: string
  detail: string
  severity: Severity
  status: string
  firstSeen?: string
  ageDays: number
  dueInDays: number
  breached: boolean
  /** Cost of ONE defect of this class, per-occurrence, not annualised. */
  defectCostUsd: number
  frameworks?: FrameworkRef[]
  evidence?: string[]
}

export interface RiskRegister {
  risks: Risk[]
  sumDefectCostUsd: number
  breached: number
  bySeverity: Record<string, number>
}

export interface EditedFile {
  file: string
  edits: number
  radius?: number
  dependents?: number
  /** False when the graph does not index this file, unknown, not low-risk. */
  known: boolean
}

export interface BlastReport {
  graphPresent: boolean
  /** Molloy-Reed kappa >= 2: the graph has a cascade-capable core. */
  percolates: boolean
  kappa?: number
  files?: EditedFile[]
  unknown: number
  runsScanned: number
  truncated?: boolean
}

export interface HeadBinary {
  headId: string
  path: string
  sha256: string
  size: number
  /** No prior fingerprint, a baseline, not a finding. */
  new?: boolean
  /** Content hash differs from the stored one. */
  changed?: boolean
  previous?: string
  firstSeen?: string
}

export interface SupplyChain {
  binaries?: HeadBinary[]
  new: number
  changed: number
  unfingerprintable?: number
}

export interface FamilyRisk {
  family: string
  /** Measured excess same-family agreement. */
  coupling: number
  critical: boolean
}

export interface SourcePower {
  source: string
  domain: string
  /** Diagnostic power in nats, expected |LLR|. ~0 is a coin flip. */
  d: number
  /** Excludes the prior, so 0 means never calibrated. */
  observations: number
}

export interface EvidenceQuality {
  runs: number
  families?: FamilyRisk[]
  weakSources?: SourcePower[]
  /** Used but never calibrated, absence of data, not measured weakness. */
  uncalibratedSources?: string[]
}

export interface ConfigEpoch {
  breadcrumb: string
  events: number
  firstTs: string
  lastTs: string
}

export interface ConfigDrift {
  epochs?: ConfigEpoch[]
  /** More than one configuration produced this log. */
  changed: boolean
  unstamped: number
}

export interface SecurityCount {
  label: string
  count: number
}

export interface Threats {
  /** Which injection phrase was actually tried. */
  byMarker?: SecurityCount[]
  /** Resources drawing repeat denials, probing. */
  probedResources?: SecurityCount[]
  /** read / write / exec / network split of risky events. */
  byAction?: SecurityCount[]
}

/** One raw ledger row, the evidence behind a finding. */
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
  /** Links the reply into Session, "why did it say that" is one click. */
  runId: string
  error?: string
  /** No heads discoverable at all, the dock offers to retry instead of
   *  showing a raw dispatch error. */
  needsProbe?: boolean
  /** Set when the task parked waiting on a human decision. Not an error: it
   *  needs you, it did not break. `taskId` is what answers it. */
  question?: string
  taskId?: string
}

/** One task parked waiting on a human decision. */
export interface PendingQuestion {
  taskId: string
  runId: string
  question: string
  head: string
  resource?: string
  prompt: string
  /** Epoch ms, the frontend formats the age itself. */
  askedAtMs: number
}

/** The parked queue, plus whether it could be read in full. */
export interface QuestionQueue {
  questions: PendingQuestion[]
  /** One unreadable file must not hide the answerable ones. */
  error?: string
}

/** One routable head as registry/models.yaml declares it. */
export interface Model {
  id: string
  name: string
  tier: number
  provider: string
  pool: string
  /** The registry's own band. Hydra has no thinking-depth dial, depth is which
   *  model you pick, so this is what a picker shows in its place. */
  complexityMin: number
  complexityMax: number
  /** Qualitative registry labels ("slow", "very_high"), not measurements. */
  speed: string
  accuracy: string
  contextWindow: number
  /** False means the registry switched it off, still listed, so a picker can
   *  grey it rather than pretend it does not exist. */
  enabled: boolean
}

/** A shared quota and the models drawing from it. */
export interface Pool {
  name: string
  /** False means one member, or members that do not contend. */
  shared: boolean
  note?: string
  models: Model[]
  /** What Hydra logged against this pool, NOT a provider-reported balance.
   *  A floor on usage, never a quota reading. */
  observedCalls: number
  observedCostUsd: number
  observedTokens: number
}

export interface ModelRegistry {
  /** False when models.yaml could not be read or parsed, an empty list then
   *  means "could not look", not "no models". */
  found: boolean
  error?: string
  pools: Pool[]
}
