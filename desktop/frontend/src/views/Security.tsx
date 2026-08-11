import { useState } from 'react'
import type { Action, Category, Check, HeadRisk, LedgerPanel, SecurityReport, Trend } from '../types'
import { coverageBand, costBand, toSecurityCSV } from '../format'
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
  const [tab, setTab] = useState<'hero' | 'detailed'>('hero')

  return (
    <>
      <header className="view__head">
        <div className="sec-headrow">
          <div>
            <h1 className="view__title">Security</h1>
            <p className="view__sub">
              Coverage against the OWASP LLM Top 10 — the percentage of applicable categories with
              a live, evidence-backed mechanism. Never a blended score.
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
          aria-current={tab === 'hero' ? 'page' : undefined}
          onClick={() => setTab('hero')}
        >
          Hero
        </button>
        <button
          className="tab"
          aria-current={tab === 'detailed' ? 'page' : undefined}
          onClick={() => setTab('detailed')}
        >
          Detailed
        </button>
      </div>

      {tab === 'hero' ? <Hero data={data} /> : <Detailed data={data} />}
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

function Hero({ data }: { data: SecurityReport }) {
  const band = coverageBand(data.coverage.percentCovered)
  const pii = findCheckStatus(data.checks, 'PII/sensitive-data detections')
  const adherence = findCheckStatus(data.checks, 'Policy adherence')

  return (
    <>
      <div className="sec-hero">
        <div className="card sec-hero__score">
          <CoverageRing percent={data.coverage.percentCovered} band={band} />
          <div className="sec-hero__note">
            {data.coverage.covered}/{data.coverage.applicable} categories covered
            <TrendLine trend={data.trend} />
          </div>
          {data.history && data.history.length >= 2 && (
            <div className="sec-hero__history">
              <CoverageHistory history={data.history} />
            </div>
          )}
        </div>
        <div className="card sec-hero__grid">
          <div className="card__label">Coverage by category</div>
          <StatusDonut segments={coverageSegments(data.coverage.categories)} />
          <CategoryGrid categories={data.coverage.categories} />
        </div>
      </div>

      <div className="cards">
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
        <div className="card">
          <div className="card__label">Bypass attempts</div>
          <div className="card__value--sm">
            {data.ledger.denied} blocked · {data.ledger.flagged} flagged
          </div>
          {data.riskHistory && data.riskHistory.length >= 2 && (
            <div className="sec-hero__history">
              <RiskTrend history={data.riskHistory} />
            </div>
          )}
        </div>
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

function Detailed({ data }: { data: SecurityReport }) {
  return (
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
