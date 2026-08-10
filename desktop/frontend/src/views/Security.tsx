import type { Category, Check, HeadRisk, SecurityReport } from '../types'
import { coverageBand } from '../format'

export function Security({ data }: { data: SecurityReport }) {
  return (
    <>
      <header className="view__head">
        <h1 className="view__title">Security</h1>
        <p className="view__sub">
          Coverage against the OWASP LLM Top 10 — the percentage of applicable categories with a
          live, evidence-backed mechanism. Never a blended score.
        </p>
      </header>

      {!data.integrityIntact && (
        <div className="error">
          INTEGRITY COMPROMISED — the ledger chain has been tampered with. Nothing else in this
          report can be trusted until this is resolved.
        </div>
      )}

      <div className="cards">
        <CoverageCard data={data} />
        <LedgerCard data={data} />
      </div>

      <Categories categories={data.coverage.categories} />
      <ByHead rows={data.byHead} />
      <Checks checks={data.checks} />
      <Recommendations lines={data.recommendations} />
    </>
  )
}

function CoverageCard({ data }: { data: SecurityReport }) {
  const { coverage, trend } = data
  const band = coverageBand(coverage.percentCovered)
  return (
    <div className={`card sec-score--${band}`}>
      <div className="card__label">OWASP LLM Top-10 coverage</div>
      <div className="card__value">{coverage.percentCovered.toFixed(0)}%</div>
      <div className="gov__track">
        <div className="gov__fill" style={{ width: `${Math.min(coverage.percentCovered, 100)}%` }} />
      </div>
      <div className="card__note">
        {coverage.covered}/{coverage.applicable} categories covered
        {trend.available && (
          <>
            {' · '}
            {trend.deltaPct > 0 ? '↑' : trend.deltaPct < 0 ? '↓' : '→'}{' '}
            {trend.deltaPct >= 0 ? '+' : ''}
            {trend.deltaPct.toFixed(0)}% since first run ({trend.firstPct.toFixed(0)}%)
          </>
        )}
      </div>
    </div>
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
              <span className={`sec-cat__status sec-cat__status--${c.status}`}>{c.status}</span>
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

function Recommendations({ lines }: { lines?: string[] }) {
  if (!lines || lines.length === 0) return null
  return (
    <section>
      <h2 className="section__title">Recommendations</h2>
      <ul className="sec-recs">
        {lines.map((l, i) => (
          <li key={i}>{l}</li>
        ))}
      </ul>
    </section>
  )
}
