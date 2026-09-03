import { useState } from 'react'
import type {
  Action,
  AgentPrivilege,
  Attestation,
  BOMEntry,
  Category,
  Check,
  ConfigDrift,
  Control,
  EvidenceQuality,
  SupplyChain,
  BlastReport,
  Incident,
  Posture,
  Risk,
  RiskRegister,
  Severity,
  Exposure,
  HeadRisk,
  LedgerEvent,
  LedgerPanel,
  PolicyAudit,
  SecurityCount,
  SecurityReport,
  Threats,
  Trend,
} from '../types'
import { clockTime, coverageBand, costBand, toSecurityCSV } from '../format'
import {
  CategoryGrid,
  CoverageHistory,
  CoverageRing,
  type DonutSegment,
  RiskTrend,
  StatusDonut,
} from './SecurityCharts'

function findCheckStatus(checks: Check[], name: string): string | undefined {
  return checks.find((c) => c.name === name)?.status
}

function coverageSegments(categories: Category[]): DonutSegment[] {
  const counts = { enforced: 0, configured: 0, gap: 0 }
  for (const c of categories) {
    if (c.status === 'enforced' || c.status === 'configured' || c.status === 'gap') counts[c.status]++
  }
  return [
    { label: 'Enforced', value: counts.enforced, colorVar: 'var(--hy-cheap)' },
    { label: 'Configured', value: counts.configured, colorVar: 'var(--hy-aqua)' },
    { label: 'Gap', value: counts.gap, colorVar: 'var(--hy-expensive)' },
  ]
}

// Allowed/Denied are mutually exclusive (Decision is one or the other) and
// sum to Total — a real part-to-whole pie. Flagged is a different,
// independent dimension (a flagged event can be either allowed or denied),
// so it can't be a third slice without double-counting; it's called out
// separately instead of drawn into the same pie.
function ledgerSegments(ledger: LedgerPanel): DonutSegment[] {
  return [
    { label: 'Allowed', value: ledger.allowed, colorVar: 'var(--hy-cheap)' },
    { label: 'Denied', value: ledger.denied, colorVar: 'var(--hy-expensive)' },
  ]
}

function actionSegments(actions: Action[]): DonutSegment[] {
  const counts = { now: 0, soon: 0, watch: 0 }
  for (const a of actions) counts[a.priority]++
  return [
    { label: 'Now', value: counts.now, colorVar: 'var(--hy-expensive)' },
    { label: 'Soon', value: counts.soon, colorVar: 'var(--hy-mid)' },
    { label: 'Watch', value: counts.watch, colorVar: 'var(--hy-dim)' },
  ]
}

export function Security({ data }: { data: SecurityReport }) {
  const [tab, setTab] = useState<'overview' | 'detailed'>('overview')

  return (
    <>
      <header className="view__head">
        <div className="sec-headrow">
          <div>
            <h1 className="view__title">Audit</h1>
            <p className="view__sub">
              What the agents on this machine did, and whether you need to act today.
            </p>
          </div>
          <button className="sec-export" onClick={() => downloadSecurityCSV(data)}>
            Export CSV
          </button>
        </div>
      </header>

      {!data.integrityIntact && (
        <div className="error">
          INTEGRITY COMPROMISED — the ledger chain has been tampered with. Nothing else in this
          report can be trusted until this is resolved.
        </div>
      )}

      <div className="tabs">
        <button
          className="tab"
          aria-current={tab === 'overview' ? 'page' : undefined}
          onClick={() => setTab('overview')}
        >
          Overview
        </button>
        <button
          className="tab"
          aria-current={tab === 'detailed' ? 'page' : undefined}
          onClick={() => setTab('detailed')}
        >
          Detailed
        </button>
      </div>

      {tab === 'overview' ? <Hero data={data} /> : <Detailed data={data} />}
    </>
  )
}

function downloadSecurityCSV(data: SecurityReport) {
  const csv = toSecurityCSV(data.coverage.categories)
  const blob = new Blob([csv], { type: 'text/csv;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `hydra-security-${new Date().toISOString().slice(0, 10)}.csv`
  a.click()
  URL.revokeObjectURL(url)
}

// ── hero: the catchy first view ────────────────────────────────────────────

// Bottom line up front. A coverage ring is a measurement; the verdict is the
// answer, and the answer goes first.
function VerdictBanner({ posture, incidents }: { posture: Posture; incidents: Incident[] }) {
  if (!posture) return null
  const cls =
    posture.verdict === 'act now' ? 'sec-verdict--act' :
    posture.verdict === 'attention' ? 'sec-verdict--attention' : 'sec-verdict--ok'
  // Stages of the incident the verdict is quoting, so the banner carries the
  // shape of the attack and not only the sentence.
  const cited = incidents.find((in_) => posture.trigger?.includes(in_.narrative))
  return (
    <div className={`sec-verdict ${cls}`}>
      <div className="sec-verdict__label">{posture.verdict}</div>
      <div className="sec-verdict__trigger">{posture.trigger}</div>
      {cited && (
        <div className="sec-verdict__stages">
          {cited.stages.map((st) => (
            <span className="sec-cat__status sec-cat__status--gap" key={st}>{st}</span>
          ))}
          <span className="sec-action__age">
            likelihood {cited.likelihood} × impact {cited.impact} · {cited.events?.length ?? 0} event(s)
          </span>
        </div>
      )}
      {(posture.because?.length ?? 0) > 1 && (
        <div className="sec-verdict__scope">
          +{(posture.because?.length ?? 1) - 1} more condition(s) below
        </div>
      )}
      {posture.verdict === 'ok' && posture.checked?.length > 0 && (
        <div className="sec-verdict__scope">checked: {posture.checked.join(', ')}</div>
      )}
    </div>
  )
}

// Critical and high must not read alike at a glance: critical is filled, high
// is outlined. Colour alone was doing all the work and both rendered pink.
function sevClass(s: Severity) {
  if (s === 'critical') return 'sec-sev--critical'
  if (s === 'high') return 'sec-sev--high'
  if (s === 'medium') return 'sec-sev--medium'
  return 'sec-sev--low'
}

// An incident is a story, so it renders as one — the narrative first, the
// stage chips and the evidence count under it.
function IncidentList({ incidents, heading }: { incidents: Incident[]; heading: string }) {
  if (incidents.length === 0) return null
  return (
    <section>
      <h2 className="section__title">{heading}</h2>
      <div className="sec-actions">
        {incidents.map((in_) => (
          <div className={`sec-action sec-action--${in_.severity === 'critical' || in_.severity === 'high' ? 'now' : 'soon'}`} key={in_.id}>
            <div className="sec-action__head">
              <span className={`sec-cat__status ${sevClass(in_.severity)}`}>{in_.severity}</span>
              <span className="sec-action__age">
                likelihood {in_.likelihood} × impact {in_.impact}
              </span>
            </div>
            <div className="sec-action__title">{in_.narrative}</div>
            <div className="sec-action__detail">
              {in_.stages.map((st) => (
                <span className="sec-cat__status" key={st}>{st}</span>
              ))}
              <span className="sec-action__age"> {in_.events?.length ?? 0} event(s)</span>
            </div>
          </div>
        ))}
      </div>
    </section>
  )
}

// The governed view: one table where every finding is the same kind of thing.
function RegisterTable({ register }: { register: RiskRegister }) {
  const risks = register?.risks ?? []
  if (risks.length === 0) return null
  return (
    <section>
      <h2 className="section__title">Risk register</h2>
      <p className="card__note">
        Σ modelled defect cost ${register.sumDefectCostUsd.toFixed(0)} — per-occurrence, not
        annualised{register.breached > 0 ? ` · ${register.breached} past remediation SLA` : ''}.
        Framework mappings are curated assertions, not measurements.
      </p>
      <div className="table__wrap">
        <table className="table">
          <thead>
            <tr>
              <th>Risk</th><th>Severity</th><th className="num">Due</th>
              <th className="num">Cost/defect</th><th>Frameworks <span className="sec-curated">curated</span></th>
            </tr>
          </thead>
          <tbody>
            {risks.map((k: Risk) => (
              <tr key={k.id}>
                <td>
                  {k.title}
                  <span className="sec-risk__id mono" title="stable risk ID">{k.id}</span>
                </td>
                <td><span className={`sec-cat__status ${sevClass(k.severity)}`}>{k.severity}</span></td>
                <td className={`num ${k.breached ? 'cost--expensive' : ''}`}>{k.dueInDays}d</td>
                <td className="num">${k.defectCostUsd.toFixed(0)}</td>
                <td className="card__note">
                  {(k.frameworks ?? []).map((f) => `${f.framework} ${f.control}`).join(' · ')}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}

function Hero({ data }: { data: SecurityReport }) {
  const band = coverageBand(data.coverage.percentCovered)
  const pii = findCheckStatus(data.checks, 'PII/sensitive-data detections')
  const adherence = findCheckStatus(data.checks, 'Policy adherence')

  // The verdict already quotes the incident that produced it, so re-rendering
  // that incident underneath prints the same sentence twice in the most
  // valuable pixels on the screen.
  const quoted = data.posture?.trigger ?? ''
  const others = (data.incidents ?? []).filter((in_) => !quoted.includes(in_.narrative))

  return (
    <>
      {/* Verdict and measurement on one row: the answer is what you read, the
          charts are what you check it against. Neither is below the fold. */}
      <div className="sec-top">
        <VerdictBanner posture={data.posture} incidents={data.incidents ?? []} />
        <div className="card sec-top__score">
          <CoverageRing percent={data.coverage.percentCovered} band={band} />
          <div className="sec-hero__note">
            {data.coverage.covered}/{data.coverage.applicable} covered
            <TrendLine trend={data.trend} />
          </div>
          {data.history && data.history.length >= 2 && (
            <div className="sec-hero__history">
              <CoverageHistory history={data.history} />
            </div>
          )}
        </div>
        <div className="card sec-top__risk">
          <div className="card__label">Blocked · flagged</div>
          <div className="card__value--sm">
            {data.ledger.denied} blocked · {data.ledger.flagged} flagged
          </div>
          {data.riskHistory && data.riskHistory.length >= 2 ? (
            <div className="sec-hero__history">
              <RiskTrend history={data.riskHistory} />
            </div>
          ) : (
            <p className="card__note">No history yet — a trend appears after the second day.</p>
          )}
        </div>
      </div>

      <IncidentList incidents={others} heading={others.length === (data.incidents ?? []).length ? 'Incidents' : 'Other incidents'} />

      <div className="sec-hero">
        <div className="card sec-hero__grid">
          <div className="card__label">Coverage by category</div>
          <StatusDonut segments={coverageSegments(data.coverage.categories)} />
          <CategoryGrid categories={data.coverage.categories} />
        </div>
        {/* Rendering the column unconditionally left half the row empty on
            any machine where these checks have not run. */}
        {(pii || adherence) && (
          <div className="cards cards--stack">
            {pii && (
              <div className="card">
                <div className="card__label">PII/sensitive-data detections</div>
                <div className="card__value--sm">{pii}</div>
              </div>
            )}
            {adherence && (
              <div className="card">
                <div className="card__label">Policy adherence</div>
                <div className="card__value--sm">{adherence}</div>
              </div>
            )}
          </div>
        )}
      </div>

      <section>
        <h2 className="section__title">Top actions</h2>
        <ActionCards actions={(data.actions ?? []).slice(0, 3)} />
      </section>
    </>
  )
}

function TrendLine({ trend }: { trend: Trend }) {
  if (!trend.available) return null
  const arrow = trend.deltaPct > 0 ? '↑' : trend.deltaPct < 0 ? '↓' : '→'
  const cls = trend.deltaPct > 0 ? 'delta--good' : trend.deltaPct < 0 ? 'delta--bad' : ''
  return (
    <div className={`sec-trend ${cls}`}>
      {arrow} {trend.deltaPct >= 0 ? '+' : ''}
      {trend.deltaPct.toFixed(0)}% since first run ({trend.firstPct.toFixed(0)}%)
    </div>
  )
}

function ActionCards({ actions }: { actions: Action[] }) {
  if (actions.length === 0) {
    return (
      <p className="card__note">
        Nothing to act on — every applicable category is covered and no head is flagged.
      </p>
    )
  }
  return (
    <div className="sec-actions">
      {actions.map((a) => (
        <div className={`sec-action sec-action--${a.priority}`} key={`${a.kind}-${a.id}`}>
          <div className="sec-action__head">
            <span className="sec-action__priority">{a.priority}</span>
            {a.ageDays > 0 && <span className="sec-action__age">{a.ageDays}d</span>}
          </div>
          <div className="sec-action__title">{a.title}</div>
          <div className="sec-action__detail">{a.detail}</div>
        </div>
      ))}
    </div>
  )
}

// ── detailed: the full breakdown ───────────────────────────────────────────

const DETAIL_TABS = [
  { id: 'register', label: 'Register' },
  { id: 'coverage', label: 'Coverage' },
  { id: 'controls', label: 'Controls' },
  { id: 'policy', label: 'Policy' },
  { id: 'exposure', label: 'Exposure' },
  { id: 'threats', label: 'Threats' },
  { id: 'access', label: 'Access' },
  { id: 'estate', label: 'Estate' },
  { id: 'evidence', label: 'Evidence' },
  { id: 'attest', label: 'Attestation' },
] as const

type DetailTab = (typeof DETAIL_TABS)[number]['id']

// Sub-tabs rather than one long scroll: these are five different questions
// (what's covered / is the policy sound / did data leak / what was attempted /
// show me the rows), and stacking them made the view read as a list of lists.
function Detailed({ data }: { data: SecurityReport }) {
  const [tab, setTab] = useState<DetailTab>('register')
  return (
    <>
      <div className="tabs">
        {DETAIL_TABS.map((t) => (
          <button
            key={t.id}
            className="tab"
            aria-current={tab === t.id ? 'page' : undefined}
            onClick={() => setTab(t.id)}
          >
            {t.label}
          </button>
        ))}
      </div>

      {tab === 'register' && <RegisterTable register={data.register} />}

      {tab === 'coverage' && (
        <>
          <div className="cards">
            <LedgerCard data={data} />
          </div>
          <Categories categories={data.coverage.categories} />
          <ByHead rows={data.byHead} />
          <Checks checks={data.checks} />
          <section>
            <h2 className="section__title">Full action queue</h2>
            {(data.actions ?? []).length > 0 && (
              <div className="card">
                <div className="card__label">By priority</div>
                <StatusDonut segments={actionSegments(data.actions ?? [])} />
              </div>
            )}
            <ActionCards actions={data.actions ?? []} />
          </section>
        </>
      )}
      {tab === 'controls' && (
        <>
          <ControlsView controls={data.controls ?? []} />
          <ConfidenceEvidenceView evidence={data.evidence} />
          <SupplyChainView supply={data.supplyChain} />
          <BlastView blast={data.blast} />
        </>
      )}
      {tab === 'policy' && (
        <>
          <DriftView drift={data.drift} />
          <PolicyAuditView audit={data.policyAudit} />
        </>
      )}
      {tab === 'exposure' && <ExposureView exposures={data.exposures ?? []} />}
      {tab === 'threats' && <ThreatsView threats={data.threats} />}
      {tab === 'access' && <PrivilegeView rows={data.privilege ?? []} />}
      {tab === 'estate' && <BOMView entries={data.bom ?? []} />}
      {tab === 'evidence' && <EvidenceView events={data.events ?? []} truncated={!!data.truncated} />}
      {tab === 'attest' && <AttestationView a={data.attestation} />}
    </>
  )
}

// Least privilege: what an agent actually touched against what a rule scopes.
// An unscoped agent that changes state leads, because that is the finding.
function PrivilegeView({ rows }: { rows: AgentPrivilege[] }) {
  if (rows.length === 0) {
    return <p className="card__note">No ledger event names an agent, so there is no footprint to review.</p>
  }
  return (
    <section>
      <h2 className="section__title">Agent entitlements</h2>
      <p className="card__note">
        Hydra&rsquo;s default is allow, so an agent no rule names is governed only by that
        default. Unscoped agents that change state are listed first.
      </p>
      <div className="table__wrap">
        <table className="table">
          <thead>
            <tr>
              <th>Agent</th><th>Scope</th><th className="num">Allowed</th><th className="num">Denied</th>
              <th className="num">State-changing</th><th>Touched</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((p) => (
              <tr key={p.agent}>
                <td className="mono">{p.agent}</td>
                <td>
                  <span className={`sec-cat__status ${p.unscoped ? 'sec-sev--high' : 'sec-cat__status--enforced'}`}>
                    {p.unscoped ? 'unscoped' : 'scoped'}
                  </span>
                </td>
                <td className="num">{p.allowed}</td>
                <td className="num">{p.denied}</td>
                <td className={`num ${p.unscoped && p.writesOrExecs > 0 ? 'cost--expensive' : ''}`}>
                  {p.writesOrExecs}
                </td>
                <td className="card__note">
                  {(p.actions ?? []).join(', ') || '—'}
                  {(p.resources ?? []).length > 0 && ` · ${p.resources!.length} resource(s)`}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}

// The AI-BOM: an inventory of what is installed is less useful than one that
// says what is live and what leaves the machine.
function BOMView({ entries }: { entries: BOMEntry[] }) {
  if (entries.length === 0) {
    return <p className="card__note">No heads discovered — run <code>hyctl probe</code>.</p>
  }
  const remote = entries.filter((b) => !b.local).length
  const runtime = entries.filter((b) => b.origin === 'user').length
  return (
    <section>
      <h2 className="section__title">Model estate (AI-BOM)</h2>
      <p className="card__note">
        {entries.length} head(s) · {remote} route off-machine
        {runtime > 0 ? ` · ${runtime} added at runtime rather than from the curated catalog` : ''}
      </p>
      <div className="table__wrap">
        <table className="table">
          <thead>
            <tr>
              <th>Head</th><th>Provider</th><th>Source</th><th>Origin</th>
              <th>Egress</th><th>Live</th><th>Fingerprint</th>
            </tr>
          </thead>
          <tbody>
            {entries.map((b) => (
              <tr key={b.headId}>
                <td className="mono">{b.headId}</td>
                <td>{b.provider || '—'}</td>
                <td>{b.source || '—'}</td>
                <td>{b.origin === 'user' ? 'runtime' : 'builtin'}</td>
                <td>
                  <span className={`sec-cat__status ${b.local ? 'sec-cat__status--enforced' : 'sec-sev--medium'}`}>
                    {b.local ? 'local' : 'remote'}
                  </span>
                </td>
                <td className="card__note">{b.used ? 'used' : 'idle'}</td>
                <td className="mono card__note">{b.fingerprint ? b.fingerprint.slice(0, 12) : '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}

// The attestation is only worth something if a reader can check it without
// trusting the reporter, so the evidence state leads and never hides.
function AttestationView({ a }: { a: Attestation }) {
  if (!a || !a.digest) {
    return <p className="card__note">No attestation was produced for this run.</p>
  }
  const trustworthy =
    a.evidence.chainIntact &&
    !a.evidence.truncated &&
    !a.evidence.anchorMissing &&
    !(a.evidence.events > 0 && a.evidence.chainedEvents === 0)
  return (
    <section>
      <h2 className="section__title">Attestation</h2>
      {!trustworthy && (
        <div className="error">
          The audit log underneath this attestation cannot be independently checked, so the
          claims above rest on evidence that is not tamper-evident.
        </div>
      )}
      <div className="cards">
        <div className="card">
          <div className="card__label">Posture</div>
          <div className="card__value--sm">{a.verdict}</div>
          <p className="card__note">{a.trigger}</p>
        </div>
        <div className="card">
          <div className="card__label">Open risks</div>
          <div className="card__value--sm">
            {a.openRisks}
            {a.slaBreached > 0 ? ` · ${a.slaBreached} past SLA` : ''}
          </div>
        </div>
        <div className="card">
          <div className="card__label">Evidence</div>
          <div className="card__value--sm">
            {a.evidence.events} event(s), {a.evidence.chainedEvents} chained
          </div>
          <p className="card__note">
            {a.evidence.truncated
              ? 'truncated — records were deleted from the end'
              : a.evidence.anchorMissing
                ? 'unanchored — truncation would not be detected'
                : a.evidence.chainIntact
                  ? 'chain intact'
                  : 'chain broken — the log was modified after recording'}
          </p>
        </div>
      </div>
      <p className="card__note" style={{ marginTop: 12 }}>
        Generated {clockTime(a.generatedAt)} by {a.tool} {a.version}
        {a.configFingerprint ? ` · rules in force ${a.configFingerprint.slice(0, 12)}` : ''}
      </p>
      <p className="card__note">
        Digest <span className="mono">{a.digest.slice(0, 16)}</span> covers every claim above, so two
        copies can be compared without trusting either holder. Deliberately unsigned: Hydra has no
        key management, and a signature without one would be theatre.
      </p>
    </section>
  )
}

// "Is it configured" is the easy question; this is the useful one. A control
// that is declared but cannot fire reads as protection everywhere it is
// listed while doing nothing, so inert rows lead.
function ControlsView({ controls }: { controls: Control[] }) {
  if (controls.length === 0) {
    return <p className="card__note">No controls were audited.</p>
  }
  const status = (c: Control) =>
    !c.declared ? 'absent' : !c.wired ? 'inert' : c.limited ? 'limited' : 'active'
  const pill = (s: string) =>
    s === 'active' ? 'sec-cat__status--enforced' : s === 'inert' ? 'sec-cat__status--gap' : ''
  const rank = (c: Control) => (status(c) === 'inert' ? 0 : status(c) === 'limited' ? 1 : 2)
  const rows = [...controls].sort((a, b) => rank(a) - rank(b))

  return (
    <section>
      <h2 className="section__title">Control effectiveness</h2>
      <div className="table__wrap">
        <table className="table">
          <thead>
            <tr>
              <th>Control</th>
              <th>State</th>
              <th>Detail</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((c) => {
              const s = status(c)
              return (
                <tr key={c.name}>
                  <td>
                    {c.name}
                    {/* Say which claims are observed and which were read off
                        the source, rather than presenting both as evidence. */}
                    {!c.verified && <span className="sec-cat__status">source-derived</span>}
                  </td>
                  <td>
                    <span className={`sec-cat__status ${pill(s)}`}>{s}</span>
                  </td>
                  <td className="card__note">{c.detail}</td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </section>
  )
}

// A confidence figure assembled from correlated or coin-flip sources reads
// as a result while being none — the opposite of a missing control, which at
// least looks missing.
// A head binary changing under you is the rug-pull pattern itself. First
// sight is a baseline, not a finding — flagging every head on a first run
// would teach the reader to ignore the column.
// Consequences, not access decisions: an edit to a hub forty files depend on
// is a different risk from an edit to a leaf. A file the graph does not index
// is shown as unknown and sorted last — never as low-risk, which is the whole
// reason internal/graph exposes Knows().
function BlastView({ blast }: { blast: BlastReport }) {
  if (!blast?.graphPresent) {
    return (
      <section>
        <h2 className="section__title">Edit blast radius</h2>
        <p className="card__note">
          No <code>graph.json</code>, so the reach of an agent's edits cannot be scored. Generate one
          with <code>hyctl graph</code>.
        </p>
      </section>
    )
  }
  const files = blast.files ?? []
  if (files.length === 0) {
    return (
      <section>
        <h2 className="section__title">Edit blast radius</h2>
        <p className="card__note">No agent edit was found in the {blast.runsScanned} most recent run(s).</p>
      </section>
    )
  }
  const max = Math.max(...files.map((f) => f.radius ?? 0), 1)
  return (
    <section>
      <h2 className="section__title">Edit blast radius</h2>
      {blast.percolates && (
        <p className="card__note">
          The dependency graph percolates (kappa {blast.kappa?.toFixed(1)}), so an edit to a hub can
          cascade.
        </p>
      )}
      <div className="table__wrap">
        <div className="rank">
          {files.map((f) => (
            <div className="rank__row" key={f.file}>
              <span className="rank__name" title={f.file}>
                {f.file.split('/').pop()}
              </span>
              <span className="rank__track">
                <span
                  className={`rank__fill rank__fill--${
                    !f.known ? 'free' : (f.dependents ?? 0) > 0 ? 'expensive' : 'cheap'
                  }`}
                  style={{ width: f.known ? `${((f.radius ?? 0) / max) * 100}%` : '0%' }}
                />
              </span>
              <span className="rank__value">
                {f.known ? `${f.dependents} dep · ${f.radius?.toFixed(2)}×` : 'unknown'}
              </span>
            </div>
          ))}
        </div>
      </div>
      {blast.truncated && (
        <p className="card__note">Showing the {blast.runsScanned} most recent runs; older runs were not scanned.</p>
      )}
    </section>
  )
}

function SupplyChainView({ supply }: { supply: SupplyChain }) {
  const bins = supply?.binaries ?? []
  if (bins.length === 0) {
    return (
      <section>
        <h2 className="section__title">Head binaries</h2>
        <p className="card__note">No CLI-sourced head was discovered, so there is no local binary to fingerprint.</p>
      </section>
    )
  }
  return (
    <section>
      <h2 className="section__title">Head binaries</h2>
      {supply.changed > 0 && (
        <div className="error">
          {supply.changed} head binary(ies) changed since last seen. An upgrade and a swap look
          identical here — confirm which it was.
        </div>
      )}
      <div className="table__wrap">
        <table className="table">
          <thead>
            <tr>
              <th>Head</th>
              <th>State</th>
              <th>Fingerprint</th>
              <th>Path</th>
            </tr>
          </thead>
          <tbody>
            {bins.map((b) => (
              <tr key={b.headId}>
                <td>{b.headId}</td>
                <td>
                  <span
                    className={`sec-cat__status ${
                      b.changed ? 'sec-cat__status--gap' : b.new ? '' : 'sec-cat__status--enforced'
                    }`}
                  >
                    {b.changed ? 'changed' : b.new ? 'baselined' : 'unchanged'}
                  </span>
                </td>
                <td className="mono">{b.sha256.slice(0, 12)}</td>
                <td className="mono">{b.path}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}

function ConfidenceEvidenceView({ evidence }: { evidence: EvidenceQuality }) {
  if (!evidence || evidence.runs === 0) {
    return (
      <section>
        <h2 className="section__title">Confidence evidence</h2>
        <p className="card__note">No confidence run has been recorded, so no confidence has been claimed.</p>
      </section>
    )
  }
  const families = evidence.families ?? []
  const weak = evidence.weakSources ?? []
  const uncal = evidence.uncalibratedSources ?? []
  return (
    <section>
      <h2 className="section__title">Confidence evidence</h2>
      {families.length === 0 && weak.length === 0 && (
        <p className="card__note">
          {evidence.runs} run(s); no correlated families and no source measured as undiagnostic.
        </p>
      )}
      {families.map((f) => (
        <div className="sec-action sec-action--now" key={f.family}>
          <div className="sec-action__title">{f.family} heads vote as one</div>
          <div className="sec-action__detail">
            they agree {(f.coupling * 100).toFixed(0)}% beyond chance, so an ensemble of them reports
            more confidence than it earned
          </div>
        </div>
      ))}
      {weak.map((w) => (
        <div className="sec-action sec-action--soon" key={w.source}>
          <div className="sec-action__title">{w.source} carries no diagnostic weight</div>
          <div className="sec-action__detail">
            D={w.d.toFixed(3)} nats over {w.observations.toFixed(0)} recorded outcomes — its agreement
            barely moves the posterior
          </div>
        </div>
      ))}
      {uncal.length > 0 && (
        <p className="card__note">
          {/* Never rendered as weakness: zero observations is no measurement,
              not a bad one. */}
          Uncalibrated (weight rests on the prior): {uncal.join(', ')}
        </p>
      )}
    </section>
  )
}

function DriftView({ drift }: { drift: ConfigDrift }) {
  const epochs = drift?.epochs ?? []
  if (epochs.length === 0) {
    return null
  }
  if (!drift.changed) {
    return (
      <p className="card__note">
        All {epochs[0].events} stamped event(s) were recorded under one configuration ({epochs[0].breadcrumb}).
      </p>
    )
  }
  return (
    <section>
      <h2 className="section__title">Configuration history</h2>
      <div className="error">
        The routing/pricing configuration changed mid-history — decisions recorded before{' '}
        {epochs[epochs.length - 1].firstTs} were made under different rules than those after it.
      </div>
      <div className="table__wrap">
        <table className="table">
          <thead>
            <tr>
              <th>Configuration</th>
              <th className="num">Events</th>
              <th>From</th>
              <th>To</th>
            </tr>
          </thead>
          <tbody>
            {epochs.map((e) => (
              <tr key={e.breadcrumb}>
                <td className="mono">{e.breadcrumb}</td>
                <td className="num">{e.events}</td>
                <td className="mono">{e.firstTs || '—'}</td>
                <td className="mono">{e.lastTs || '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}

function PolicyAuditView({ audit }: { audit: PolicyAudit }) {
  if (!audit || audit.rules.length === 0) {
    return (
      <>
        <FailOpenBanner audit={audit} />
        <p className="card__note">
          No rules are defined — every access falls through to the {audit?.default ?? 'allow'} default,
          so nothing is scoped.
        </p>
      </>
    )
  }
  const max = Math.max(...audit.rules.map((r) => r.hits), 1)
  return (
    <>
      <FailOpenBanner audit={audit} />
      <section>
        <h2 className="section__title">Rules</h2>
        <div className="table__wrap">
          <table className="table">
            <thead>
              <tr>
                <th>#</th>
                <th>Rule</th>
                <th>Decides</th>
                <th className="num">Hits</th>
                <th>Status</th>
              </tr>
            </thead>
            <tbody>
              {audit.rules.map((r) => (
                <tr key={r.index}>
                  <td className="mono">{r.index}</td>
                  <td className="mono">{r.summary}</td>
                  <td className={r.decision === 'deny' ? 'cost--expensive' : 'cost--cheap'}>{r.decision}</td>
                  <td className="num">
                    {/* An inline proportional bar so a rule doing all the
                        work is visible without reading every number. */}
                    <span className="sec-hitbar">
                      <span className="sec-hitbar__fill" style={{ width: `${(r.hits / max) * 100}%` }} />
                    </span>
                    {r.hits}
                  </td>
                  <td>
                    {r.shadowedBy !== undefined ? (
                      <span className="sec-cat__status sec-cat__status--gap">unreachable · #{r.shadowedBy} wins</span>
                    ) : r.dead ? (
                      <span className="sec-cat__status">never matched</span>
                    ) : (
                      <span className="sec-cat__status sec-cat__status--enforced">active</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <p className="card__note">
          {audit.defaultHits} access(es) fell through every rule to the {audit.default} default.
        </p>
      </section>
    </>
  )
}

function FailOpenBanner({ audit }: { audit: PolicyAudit }) {
  if (!audit?.failOpen) return null
  return (
    <div className="error">
      FAIL-OPEN — the default decision is allow, so anything no rule names is permitted. Set
      "default": "deny" in the policy to invert that.
    </div>
  )
}

function ExposureView({ exposures }: { exposures: Exposure[] }) {
  if (exposures.length === 0) {
    return <p className="card__note">No sensitive data has been detected in any recorded access.</p>
  }
  return (
    <section>
      <h2 className="section__title">Sensitive data exposure</h2>
      <div className="table__wrap">
        <table className="table">
          <thead>
            <tr>
              <th>Destination</th>
              <th>Head</th>
              <th>Resource</th>
              <th>Detected</th>
              <th>Agent</th>
            </tr>
          </thead>
          <tbody>
            {exposures.map((e, i) => (
              <tr key={`${e.ts}-${i}`}>
                <td>
                  {/* Confirmed remote and merely-unidentified are different
                      claims and must not look the same: an offline head is
                      treated as remote (fail-closed) but is not evidence. */}
                  {e.remote && e.known ? (
                    <span className="sec-cat__status sec-cat__status--gap">remote</span>
                  ) : e.remote ? (
                    <span className="sec-cat__status">unidentified</span>
                  ) : (
                    <span className="sec-cat__status sec-cat__status--enforced">local</span>
                  )}
                </td>
                <td className="mono">{e.head}</td>
                <td className="mono">{e.resource || '—'}</td>
                <td>{e.piiTypes?.length ? e.piiTypes.join(', ') : 'unclassified type'}</td>
                <td className="mono">{e.agent || '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}

function ThreatsView({ threats }: { threats: Threats }) {
  const empty =
    !threats?.byMarker?.length && !threats?.probedResources?.length && !threats?.byAction?.length
  if (empty) {
    return <p className="card__note">Nothing has been denied or flagged, so there is nothing to break down.</p>
  }
  return (
    <>
      <CountBars title="Injection markers tried" rows={threats.byMarker} />
      <CountBars title="Resources probed (repeat denials)" rows={threats.probedResources} />
      <CountBars title="By action" rows={threats.byAction} />
    </>
  )
}

// Reuses Dashboard's ranked-bar classes rather than adding a fourth bar style.
function CountBars({ title, rows }: { title: string; rows?: SecurityCount[] }) {
  if (!rows || rows.length === 0) return null
  const max = Math.max(...rows.map((r) => r.count))
  return (
    <section>
      <h2 className="section__title">{title}</h2>
      <div className="table__wrap">
        <div className="rank">
          {rows.map((r) => {
            const band = costBand(r.count, max)
            return (
              <div className="rank__row" key={r.label}>
                <span className="rank__name" title={r.label}>
                  {r.label}
                </span>
                <span className="rank__track">
                  <span
                    className={`rank__fill rank__fill--${band}`}
                    style={{ width: `${(r.count / max) * 100}%` }}
                  />
                </span>
                <span className={`rank__value cost--${band}`}>{r.count}</span>
              </div>
            )
          })}
        </div>
      </div>
    </section>
  )
}

// The raw rows behind every finding — Agent/Resource/Action/FlagReason have
// been recorded on every event since the ledger shipped and rendered nowhere
// until now.
function EvidenceView({ events, truncated }: { events: LedgerEvent[]; truncated: boolean }) {
  if (events.length === 0) {
    return <p className="card__note">The ledger is empty — nothing has been recorded on this machine.</p>
  }
  const rows = [...events].reverse() // newest first
  return (
    <section>
      <h2 className="section__title">Ledger evidence</h2>
      {truncated && (
        <p className="card__note">
          Showing the most recent {events.length} events. The full ledger is longer — read it with{' '}
          <code>hyctl mcp log</code>.
        </p>
      )}
      <div className="table__wrap">
        <table className="table">
          <thead>
            <tr>
              <th>Time</th>
              <th>Decision</th>
              <th>Action</th>
              <th>Head</th>
              <th>Resource</th>
              <th>Why</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((e, i) => (
              <tr key={`${e.ts}-${i}`}>
                <td className="mono">{e.ts ? clockTime(e.ts) : '—'}</td>
                <td className={e.decision === 'deny' ? 'cost--expensive' : 'cost--cheap'}>
                  {e.decision}
                  {e.flagged && <span className="sec-cat__status sec-cat__status--gap">flagged</span>}
                </td>
                <td className="mono">{e.action || '—'}</td>
                <td className="mono">{e.tool || '—'}</td>
                <td className="mono">{e.resource || '—'}</td>
                <td className="card__note">
                  {e.flag_reason ? `“${e.flag_reason}”` : e.reason || '—'}
                  {e.pii_types?.length ? ` · ${e.pii_types.join(', ')}` : ''}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}

function LedgerCard({ data }: { data: SecurityReport }) {
  const { ledger, hasData } = data
  if (!hasData) {
    return (
      <div className="card">
        <div className="card__label">Ledger activity</div>
        <div className="card__value card__value--unknown">no data yet</div>
        <div className="card__note">No MCP access has been recorded on this machine.</div>
      </div>
    )
  }
  return (
    <div className="card">
      <div className="card__label">Ledger activity</div>
      <div className="card__value">{ledger.total}</div>
      <StatusDonut segments={ledgerSegments(ledger)} />
      {ledger.flagged > 0 && (
        <div className="card__note">
          {ledger.flagged} of these were also flagged for a suspected injection marker
          (flagged is independent of allow/deny, not a third slice above)
        </div>
      )}
    </div>
  )
}

function Categories({ categories }: { categories: Category[] }) {
  const applicable = categories.filter((c) => c.status !== 'n/a')
  if (applicable.length === 0) return null
  return (
    <section>
      <h2 className="section__title">OWASP LLM Top 10</h2>
      <div className="table__wrap">
        <ul className="sec-cats">
          {applicable.map((c) => (
            <li className="sec-cat" key={c.id}>
              <span className="sec-cat__id">{c.id}</span>
              <span className="sec-cat__name">{c.name}</span>
              <span className={`sec-cat__status sec-cat__status--${c.status}`}>
                {c.status}
                {c.status === 'gap' && (c.gapAgeDays ?? 0) > 0 && ` ${c.gapAgeDays}d`}
              </span>
              <span className="sec-cat__detail">{c.detail}</span>
            </li>
          ))}
        </ul>
      </div>
    </section>
  )
}

// Ranked bars, not a table — the same .rank/.rank__* pattern (and the
// generic free/cheap/mid/expensive ramp) Dashboard's RankedBars already
// established for exactly this shape: a handful of named rows ranked by one
// number, which a bar makes comparable at a glance in a way a table doesn't.
function ByHead({ rows }: { rows: HeadRisk[] }) {
  if (rows.length === 0) {
    return (
      <section>
        <h2 className="section__title">Per-head risk</h2>
        <p className="card__note">Nothing denied or flagged yet.</p>
      </section>
    )
  }
  const max = Math.max(...rows.map((r) => r.denied + r.flagged))
  return (
    <section>
      <h2 className="section__title">Per-head risk</h2>
      <div className="table__wrap">
        <div className="rank">
          {rows.map((r) => {
            const total = r.denied + r.flagged
            const band = costBand(total, max)
            return (
              <div className="rank__row" key={r.head}>
                <span className="rank__name" title={r.head}>
                  {r.head}
                </span>
                <span className="rank__track">
                  <span className={`rank__fill rank__fill--${band}`} style={{ width: `${(total / max) * 100}%` }} />
                </span>
                <span className={`rank__value cost--${band}`}>
                  {r.denied} denied · {r.flagged} flagged
                </span>
              </div>
            )
          })}
        </div>
      </div>
    </section>
  )
}

function Checks({ checks }: { checks: Check[] }) {
  if (checks.length === 0) return null
  return (
    <section>
      <h2 className="section__title">Checks</h2>
      <div className="cards">
        {checks.map((c) => (
          <div className="card" key={c.name}>
            <div className="card__label">{c.name}</div>
            <div className={`card__value--sm ${c.status === 'BROKEN' ? 'cost--expensive' : ''}`}>
              {c.status}
            </div>
            <div className="card__note">{c.detail}</div>
          </div>
        ))}
      </div>
    </section>
  )
}
