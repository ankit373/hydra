import type { Breakdown, Dashboard as DashboardData } from '../types'
import { clockTime, costBand, govBand, ms, pct, tokens, usd, usdExact } from '../format'

export function Dashboard({ data }: { data: DashboardData }) {
  return (
    <>
      <header className="view__head">
        <h1 className="view__title">Dashboard</h1>
        <p className="view__sub">Spend, governor pressure, and the trust ensemble's record.</p>
      </header>

      <div className="cards">
        <SpendCard data={data} />
        <GovernorCard data={data} />
        <TrustCard data={data} />
      </div>

      {data.hasData ? <Breakdowns data={data} /> : <EmptyState />}
    </>
  )
}

function SpendCard({ data }: { data: DashboardData }) {
  const { spend, hasData } = data
  if (!hasData) {
    return (
      <div className="card">
        <div className="card__label">Spend today</div>
        <div className="card__value card__value--unknown">no data yet</div>
        <div className="card__note">Nothing has dispatched on this machine.</div>
      </div>
    )
  }
  return (
    <div className="card">
      <div className="card__label">Spend today</div>
      <div className="card__value">{usd(spend.todayUsd)}</div>
      <div className="card__note">
        {spend.todayCalls} call{spend.todayCalls === 1 ? '' : 's'} · {usd(spend.allTimeUsd)} all time
        {' · '}
        {/* Provenance belongs next to the figure: it says how much of this
            number is measured rather than estimated from char/4. */}
        {pct(spend.tokensActualPct)} of tokens provider-reported
      </div>
    </div>
  )
}

function GovernorCard({ data }: { data: DashboardData }) {
  const { governor } = data
  if (!governor.known) {
    return (
      <div className="card">
        <div className="card__label">Orchestrator context</div>
        <div className="card__value card__value--unknown">unknown</div>
        {/* Not 0%. Nothing has reported usage, and 0% would read as headroom. */}
        <div className="card__note">No orchestrator has reported its usage.</div>
      </div>
    )
  }
  const band = govBand(governor.pct)
  return (
    <div className={`card gov--${band}`}>
      <div className="card__label">Orchestrator context</div>
      <div className="card__value">{pct(governor.pct)}</div>
      <div className="gov__track">
        <div className="gov__fill" style={{ width: `${Math.min(governor.pct, 100)}%` }} />
      </div>
      <div className="card__note">mode: {governor.mode}</div>
    </div>
  )
}

function TrustCard({ data }: { data: DashboardData }) {
  const { trust } = data
  if (trust.runs === 0) {
    return (
      <div className="card">
        <div className="card__label">Trust ensemble</div>
        <div className="card__value card__value--unknown">no runs</div>
        <div className="card__note">
          Try <code>hyctl dispatch --confidence 0.95</code>
        </div>
      </div>
    )
  }
  return (
    <div className="card">
      <div className="card__label">Trust ensemble</div>
      <div className="card__value">{trust.meanSamples.toFixed(2)}</div>
      <div className="card__note">
        mean samples vs fixed-{trust.fixedSwarmN} swarm · {pct(trust.samplesSavedPct)} saved ·{' '}
        {trust.runs} run{trust.runs === 1 ? '' : 's'}
      </div>
    </div>
  )
}

function Breakdowns({ data }: { data: DashboardData }) {
  return (
    <>
      <div className="grid-2">
        <BreakdownTable title="By model" heading="Model" rows={data.byModel} />
        <BreakdownTable title="By tier" heading="Tier" rows={data.byTier} />
      </div>
      <BreakdownTable title="By day" heading="Day" rows={data.byDay} />
      <RecentTable data={data} />
    </>
  )
}

function BreakdownTable({
  title,
  heading,
  rows,
}: {
  title: string
  heading: string
  rows: Breakdown[] | null
}) {
  if (!rows || rows.length === 0) return null
  const max = Math.max(...rows.map((r) => r.costUsd))
  return (
    <section>
      <h2 className="section__title">{title}</h2>
      <div className="table__wrap">
        <table className="table">
          <thead>
            <tr>
              <th>{heading}</th>
              <th className="num">Calls</th>
              <th className="num">Tokens</th>
              <th className="num">Wall</th>
              <th className="num">Cost</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.key}>
                <td className="mono">{r.key}</td>
                <td className="num">{r.calls}</td>
                <td className="num">{tokens(r.promptTokens + r.responseTokens)}</td>
                <td className="num">{ms(r.wallMs)}</td>
                <td className={`num cost--${costBand(r.costUsd, max)}`}>{usdExact(r.costUsd)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}

function RecentTable({ data }: { data: DashboardData }) {
  const rows = data.recent
  if (!rows || rows.length === 0) return null
  const max = Math.max(...rows.map((r) => r.costUsd))
  return (
    <section>
      <h2 className="section__title">Recent</h2>
      <div className="table__wrap">
        <table className="table">
          <thead>
            <tr>
              <th>Time</th>
              <th>Model</th>
              <th className="num">Tier</th>
              <th className="num">Wall</th>
              <th className="num">Cost</th>
              <th>Run</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r, i) => (
              <tr key={`${r.ts}-${i}`}>
                <td className="mono">{clockTime(r.ts)}</td>
                <td>{r.model}</td>
                <td className="num">{r.tier === 0 ? '—' : r.tier}</td>
                <td className="num">{ms(r.wallMs)}</td>
                <td className={`num cost--${costBand(r.costUsd, max)}`}>{usdExact(r.costUsd)}</td>
                <td className="mono">{r.runId || '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}

function EmptyState() {
  return (
    <div className="empty" style={{ marginTop: 26 }}>
      <p className="empty__title">Nothing has dispatched yet</p>
      <p>
        Run <code>hyctl dispatch --enum SIMPLE "…"</code> and this fills in.
      </p>
    </div>
  )
}
