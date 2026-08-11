import { useState } from 'react'
import type { Action, Category, Check, HeadRisk, SecurityReport, Trend } from '../types'
import { coverageBand, toSecurityCSV } from '../format'
import { CoverageHistory, CoverageRadar, CoverageRing } from './SecurityCharts'

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
        <div className="card sec-hero__radar">
          <div className="card__label">Coverage shape</div>
          <CoverageRadar categories={data.coverage.categories} />
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
      <div className="card__note">
        {ledger.allowed} allowed · {ledger.denied} denied · {ledger.flagged} flagged
      </div>
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

function ByHead({ rows }: { rows: HeadRisk[] }) {
  return (
    <section>
      <h2 className="section__title">Per-head risk</h2>
      {rows.length === 0 ? (
        <p className="card__note">Nothing denied or flagged yet.</p>
      ) : (
        <div className="table__wrap">
          <table className="table">
            <thead>
              <tr>
                <th>Head</th>
                <th className="num">Denied</th>
                <th className="num">Flagged</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.head}>
                  <td className="mono">{r.head}</td>
                  <td className="num">{r.denied}</td>
                  <td className="num">{r.flagged}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
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
