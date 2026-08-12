import { useEffect, useRef, useState } from 'react'
import type { Breakdown, CalibrationRow, Dashboard as DashboardData, TrustPanel } from '../types'
import { clockTime, costBand, govBand, ms, pct, sourceKind, sourceLabel, tokens, usd, usdExact } from '../format'
import { ArcGauge, Sparkline, SpendTrend, TrustArc } from './DashboardCharts'
import { useCountUp, useReveal } from '../reveal'

/**
 * `data` is null until App.tsx's first `GetDashboard()` resolves — the
 * skeleton below covers exactly that window, driven by real state rather
 * than a fake timeout. Once data lands the HUD chrome (corner brackets,
 * scanline, vignette) stays mounted for the lifetime of the view; only the
 * body swaps from skeleton to `DashboardContent`.
 */
export function Dashboard({ data }: { data: DashboardData | null }) {
  return (
    <>
      <HudChrome />
      {data ? <DashboardContent data={data} /> : <DashboardSkeleton />}
    </>
  )
}

/** Corner brackets, scanline, vignette — brand/living/hydra-hud.src.html's
 * frame language, scoped to this view rather than the shared app shell. */
function HudChrome() {
  return (
    <>
      <div className="hud-corner hud-corner--tl" aria-hidden="true" />
      <div className="hud-corner hud-corner--tr" aria-hidden="true" />
      <div className="hud-corner hud-corner--bl" aria-hidden="true" />
      <div className="hud-corner hud-corner--br" aria-hidden="true" />
      <div className="hud-scanline" aria-hidden="true" />
      <div className="hud-vignette" aria-hidden="true" />
    </>
  )
}

function DashboardHeader() {
  return (
    <header className="view__head">
      <h1 className="view__title view__title--brand">Dashboard</h1>
      <p className="view__sub">Spend, governor pressure, and the trust ensemble's record.</p>
    </header>
  )
}

function DashboardSkeleton() {
  return (
    <>
      <DashboardHeader />
      <div className="cards">
        <SkeletonCard />
        <SkeletonCard />
        <SkeletonCard />
      </div>
      <section>
        <h2 className="section__title">Spend by day</h2>
        <div className="spend-chart">
          <div className="skeleton-block skeleton-block--chart" />
        </div>
      </section>
    </>
  )
}

function SkeletonCard() {
  return (
    <div className="card">
      <div className="skeleton-block" style={{ width: '50%', height: 10 }} />
      <div className="skeleton-block" style={{ width: '72%', height: 27, marginTop: 11 }} />
      <div className="skeleton-block" style={{ width: '85%', height: 12, marginTop: 9 }} />
    </div>
  )
}

function DashboardContent({ data }: { data: DashboardData }) {
  const [drawerRow, setDrawerRow] = useState<Breakdown | null>(null)
  const [drawerOpen, setDrawerOpen] = useState(false)

  const openDrawer = (row: Breakdown) => {
    setDrawerRow(row)
    setDrawerOpen(true)
  }
  const closeDrawer = () => setDrawerOpen(false)

  useEffect(() => {
    if (!drawerOpen) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') closeDrawer()
    }
    addEventListener('keydown', onKey)
    return () => removeEventListener('keydown', onKey)
  }, [drawerOpen])

  return (
    <div className="dashboard-content">
      <DashboardHeader />

      <div className="cards">
        <SpendCard data={data} />
        <GovernorCard data={data} />
        <TrustCard data={data} />
      </div>

      {data.hasData ? <Breakdowns data={data} onSelectModel={openDrawer} /> : <EmptyState />}

      <CalibrationLeaderboard rows={data.calibration} />

      <ModelDetailDrawer row={drawerRow} open={drawerOpen} onClose={closeDrawer} />
    </div>
  )
}

function SpendCard({ data }: { data: DashboardData }) {
  const { spend, hasData, byDay } = data
  const revealed = useReveal(hasData)
  const displaySpend = useCountUp(spend.todayUsd, revealed)

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
      <div className="card__value">{usd(displaySpend)}</div>
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
  const revealed = useReveal(governor.known)
  const displayPct = useCountUp(governor.pct, revealed)

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
      <div className="arc-wrap">
        <ArcGauge
          fraction={Math.min(governor.pct, 100) / 100}
          color={`var(--hy-gov-${band})`}
          centerValue={`${Math.round(displayPct)}%`}
          centerLabel={governor.mode}
          revealed={revealed}
        />
      </div>
      <div className="card__note">mode: {governor.mode}</div>
    </div>
  )
}

function TrustCard({ data }: { data: DashboardData }) {
  const { trust } = data
  const revealed = useReveal(trust.runs > 0)

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
      <TrustCompare trust={trust} revealed={revealed} />
      <div className="card__note">
        {trust.runs} run{trust.runs === 1 ? '' : 's'}
      </div>
    </div>
  )
}

/** SPRT mean samples vs. a fixed-N swarm, same ring, so the saving reads at a
 * glance instead of as a sentence to parse. */
function TrustCompare({ trust, revealed }: { trust: TrustPanel; revealed: boolean }) {
  const saved = trust.samplesSavedPct >= 0
  return (
    <div className="trust-cmp">
      <div className="arc-wrap">
        <TrustArc meanSamples={trust.meanSamples} fixedSwarmN={trust.fixedSwarmN} revealed={revealed} />
        <div className="arc-legend">
          <span>
            <span className="arc-legend__dot arc-legend__dot--actual" />
            what it actually took
          </span>
          <span>
            <span className="arc-legend__dot arc-legend__dot--baseline" />
            fixed panel of {trust.fixedSwarmN}
          </span>
        </div>
      </div>
      <div className={`trust-cmp__delta ${saved ? 'trust-cmp__delta--good' : 'trust-cmp__delta--bad'}`}>
        {saved
          ? `${pct(trust.samplesSavedPct)} fewer samples than a fixed swarm`
          : `${pct(Math.abs(trust.samplesSavedPct))} more samples than a fixed swarm`}
      </div>
    </div>
  )
}

function Breakdowns({
  data,
  onSelectModel,
}: {
  data: DashboardData
  onSelectModel: (row: Breakdown) => void
}) {
  return (
    <>
      {data.byDay && data.byDay.length > 0 && (
        <section>
          <h2 className="section__title">Spend by day</h2>
          <SpendTrend days={data.byDay} />
        </section>
      )}
      <div className="grid-2">
        <RankedBars title="By model · click a row for detail" rows={data.byModel} onSelect={onSelectModel} />
        <RankedBars title="By tier" rows={data.byTier} />
      </div>
      <RecentTable data={data} />
    </>
  )
}

/** A ranked horizontal bar list: name, a proportional bar, and the exact
 * dollar amount — replaces the plain by-model/by-tier tables. Passing
 * `onSelect` turns each row into a button that opens the drill-down drawer
 * (used for "By model" only — one list is enough to prove the pattern out). */
function RankedBars({
  title,
  rows,
  onSelect,
}: {
  title: string
  rows: Breakdown[] | null
  onSelect?: (row: Breakdown) => void
}) {
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
            const inner = (
              <>
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
              </>
            )
            return onSelect ? (
              <button
                key={r.key}
                type="button"
                className="rank__row rank__row--detail"
                onClick={() => onSelect(r)}
              >
                {inner}
              </button>
            ) : (
              <div className="rank__row" key={r.key}>
                {inner}
              </div>
            )
          })}
        </div>
      </div>
    </section>
  )
}

/**
 * Which sources actually earn their stated confidence — one row per
 * (source, domain), sorted by D descending (the order internal/trust's
 * Calibrator.Report already returns, same as `hyctl trust calibration`).
 * Bars use the SpendTrend halo treatment (#414) turned sideways: a fainter
 * halo layered behind a crisp fill, both growing in via `transform: scaleX`
 * once `useReveal` fires, never `filter: blur`. Independent of `hasData` —
 * calibration history comes from `hyctl trust record`, not the cost log, so
 * it can be populated (or empty) regardless of whether anything dispatched.
 */
function CalibrationLeaderboard({ rows }: { rows: CalibrationRow[] }) {
  const revealed = useReveal(rows.length > 0)

  if (rows.length === 0) {
    return (
      <section>
        <h2 className="section__title">Calibration leaderboard</h2>
        <div className="empty" style={{ marginTop: 0 }}>
          <p className="empty__title">No calibration recorded yet</p>
          <p>
            Feed outcomes with <code>hyctl trust record</code> and this fills in.
          </p>
        </div>
      </section>
    )
  }

  const maxD = Math.max(...rows.map((r) => r.d))
  return (
    <section>
      <h2 className="section__title">Calibration leaderboard · who to trust</h2>
      <div className="table__wrap">
        <div className="rank">
          {rows.map((r) => (
            <CalibrationBar key={`${r.source} ${r.domain}`} row={r} maxD={maxD} revealed={revealed} />
          ))}
        </div>
        <div className="cal-legend">
          <span>
            <span className="cal-legend__dot cal-legend__dot--oracle" />
            test/lint verdict
          </span>
          <span>
            <span className="cal-legend__dot cal-legend__dot--model" />
            model's own answer
          </span>
        </div>
      </div>
    </section>
  )
}

// How far the halo bleeds past the crisp bar it sits behind, on each edge —
// matches SpendTrend's HALO_PAD.
const CAL_HALO_PAD = 3

function CalibrationBar({ row, maxD, revealed }: { row: CalibrationRow; maxD: number; revealed: boolean }) {
  const kind = sourceKind(row.source)
  // A source with real observations but zero diagnostic power (a coin flip)
  // still gets a sliver — the row itself is informative even at D=0.
  const widthPct = maxD > 0 ? (row.d / maxD) * 100 : row.n > 0 ? 4 : 0
  const grown = revealed ? ' grown' : ''
  return (
    <div className="cal__row">
      <span className="cal__name" title={row.source}>
        {sourceLabel(row.source)}
        <span className="cal__domain">{row.domain}</span>
      </span>
      <span className="cal__track">
        <span
          className={`cal__halo cal__halo--${kind}${grown}`}
          style={{
            width: `calc(${widthPct}% + ${CAL_HALO_PAD * 2}px)`,
            height: `calc(100% + ${CAL_HALO_PAD * 2}px)`,
            left: -CAL_HALO_PAD,
            top: -CAL_HALO_PAD,
          }}
        />
        <span className={`cal__fill cal__fill--${kind}${grown}`} style={{ width: `${widthPct}%` }} />
      </span>
      <span className="cal__value" title={`Se ${row.se.toFixed(2)} · Sp ${row.sp.toFixed(2)} · n=${row.n}`}>
        {row.d.toFixed(2)}
      </span>
    </div>
  )
}

/** Slide-in detail panel for a "By model" row — the exact Breakdown fields
 * GetDashboard already computes (Calls/PromptTokens/ResponseTokens/CostUSD/
 * WallMS), not a stub. `row` is kept around after close so the panel doesn't
 * blank out mid-slide-out; `open` alone drives the transform. */
function ModelDetailDrawer({
  row,
  open,
  onClose,
}: {
  row: Breakdown | null
  open: boolean
  onClose: () => void
}) {
  const closeRef = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    if (open) closeRef.current?.focus()
  }, [open])

  return (
    <>
      <div
        className={`drawer-backdrop${open ? ' drawer-backdrop--open' : ''}`}
        onClick={onClose}
        aria-hidden="true"
      />
      <aside
        className={`drawer${open ? ' drawer--open' : ''}`}
        role="dialog"
        aria-label="Model detail"
        aria-hidden={!open}
      >
        <button
          ref={closeRef}
          className="drawer__close"
          onClick={onClose}
          aria-label="Close"
          tabIndex={open ? 0 : -1}
        >
          &times;
        </button>
        {row && (
          <>
            <div className="drawer__title">Breakdown · by model</div>
            <h2 className="drawer__heading">{row.key}</h2>
            <div className="drawer__stat">{usdExact(row.costUsd)}</div>
            <div className="drawer__lbl">total cost</div>
            <div className="drawer__list">
              <div className="drawer__list-row">
                <span>Calls</span>
                <span>{row.calls}</span>
              </div>
              <div className="drawer__list-row">
                <span>Prompt tokens</span>
                <span>{tokens(row.promptTokens)}</span>
              </div>
              <div className="drawer__list-row">
                <span>Response tokens</span>
                <span>{tokens(row.responseTokens)}</span>
              </div>
              <div className="drawer__list-row">
                <span>Wall time</span>
                <span>{ms(row.wallMs)}</span>
              </div>
            </div>
          </>
        )}
      </aside>
    </>
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
