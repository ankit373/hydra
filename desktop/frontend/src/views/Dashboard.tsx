import type { Breakdown, Dashboard as DashboardData, TrustPanel } from '../types'
import { clockTime, costBand, govBand, ms, pct, usd, usdExact } from '../format'
import { Sparkline, SpendTrend } from './DashboardCharts'

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
  const { spend, hasData, byDay } = data
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
      {byDay && <Sparkline days={byDay} />}
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
      <TrustCompare trust={trust} />
      <div className="card__note">
        {trust.runs} run{trust.runs === 1 ? '' : 's'}
      </div>
    </div>
  )
}

/** SPRT mean samples vs. a fixed-N swarm, same axis, so the saving reads at a
 * glance instead of as a sentence to parse. */
function TrustCompare({ trust }: { trust: TrustPanel }) {
  const max = Math.max(trust.meanSamples, trust.fixedSwarmN, 1)
  const saved = trust.samplesSavedPct >= 0
  return (
    <div className="trust-cmp">
      <div className="trust-cmp__row">
        <span className="trust-cmp__label">SPRT</span>
        <span className="trust-cmp__track">
          <span
            className="trust-cmp__fill trust-cmp__fill--actual"
            style={{ width: `${(trust.meanSamples / max) * 100}%` }}
          />
        </span>
        <span className="trust-cmp__value">{trust.meanSamples.toFixed(2)}</span>
      </div>
      <div className="trust-cmp__row">
        <span className="trust-cmp__label">Fixed-{trust.fixedSwarmN}</span>
        <span className="trust-cmp__track">
          <span
            className="trust-cmp__fill trust-cmp__fill--baseline"
            style={{ width: `${(trust.fixedSwarmN / max) * 100}%` }}
          />
        </span>
        <span className="trust-cmp__value">{trust.fixedSwarmN}</span>
      </div>
      <div className={`trust-cmp__delta ${saved ? 'trust-cmp__delta--good' : 'trust-cmp__delta--bad'}`}>
        {saved
          ? `${pct(trust.samplesSavedPct)} fewer samples than a fixed swarm`
          : `${pct(Math.abs(trust.samplesSavedPct))} more samples than a fixed swarm`}
      </div>
    </div>
  )
}

function Breakdowns({ data }: { data: DashboardData }) {
  return (
    <>
      {data.byDay && data.byDay.length > 0 && (
        <section>
          <h2 className="section__title">Spend by day</h2>
          <SpendTrend days={data.byDay} />
        </section>
      )}
      <div className="grid-2">
        <RankedBars title="By model" rows={data.byModel} />
        <RankedBars title="By tier" rows={data.byTier} />
      </div>
      <RecentTable data={data} />
    </>
  )
}

/** A ranked horizontal bar list: name, a proportional bar, and the exact
 * dollar amount — replaces the plain by-model/by-tier tables. */
function RankedBars({ title, rows }: { title: string; rows: Breakdown[] | null }) {
  if (!rows || rows.length === 0) return null
  const max = Math.max(...rows.map((r) => r.costUsd))
  return (
    <section>
      <h2 className="section__title">{title}</h2>
      <div className="table__wrap">
        <div className="rank">
          {rows.map((r) => {
            const band = costBand(r.costUsd, max)
            // A day with only free/local calls still gets a sliver: present,
            // just not comparable to anything on this ramp.
            const widthPct = max > 0 ? (r.costUsd / max) * 100 : r.calls > 0 ? 4 : 0
            return (
              <div className="rank__row" key={r.key}>
                <span className="rank__name" title={r.key}>
                  {r.key}
                </span>
                <span className="rank__track">
                  <span
                    className={`rank__fill rank__fill--${band}`}
                    style={{ width: `${widthPct}%` }}
                  />
                </span>
                <span className={`rank__value cost--${band}`}>{usdExact(r.costUsd)}</span>
              </div>
            )
          })}
        </div>
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
