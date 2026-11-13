import { useEffect, useRef, useState, type ReactNode } from 'react'
import type {
  Breakdown,
  CalibrationRow,
  Dashboard as DashboardData,
  RecentCall,
  TrustPanel,
} from '../types'
import {
  calibrationLabel,
  calibrationStrength,
  calibrationWidthPct,
  clockTime,
  contextHeadroom,
  contextModeText,
  costBand,
  govBand,
  ms,
  pct,
  sourceKind,
  sourceLabel,
  tokens,
  usd,
  usdExact,
} from '../format'
import { SpendTrend } from './DashboardCharts'
import { PageHeader } from './PageHeader'
import { HydraMark } from '../brand'

// Context-budget bands are states, so they take the status roles.
const GOV_STATUS = { normal: 'success', warning: 'warning', critical: 'danger' } as const

/**
 * `data` is null until App.tsx's first `GetDashboard()` resolves; the skeleton
 * covers exactly that window, driven by real state rather than a timeout.
 *
 * Every figure is drawn as the number it is, from the first frame. The view
 * used to count up from zero once a reveal fired, and when the reveal did not
 * fire it said "$0" for today's spend while the header said $1.83.
 */
export function Dashboard({
  data,
  onOpenRun,
  routable = null,
  total = null,
}: {
  data: DashboardData | null
  /** Opens one recent request's run. Absent means the rows stay inert. */
  onOpenRun?: (runID: string) => void
  /** Models ready, from the same probe the rail reads. Null until it answers. */
  routable?: number | null
  total?: number | null
}) {
  return data ? (
    <DashboardContent data={data} onOpenRun={onOpenRun} routable={routable} total={total} />
  ) : (
    <DashboardSkeleton />
  )
}

function DashboardHeader() {
  return <PageHeader title="Usage" subtitle="Today, on this machine." />
}

function DashboardSkeleton() {
  return (
    <>
      <DashboardHeader />
      <div className="factrow">
        {[0, 1, 2, 3].map((i) => (
          <div className="fact" key={i}>
            <div className="skeleton-block" style={{ width: '50%', height: 12 }} />
            <div className="skeleton-block" style={{ width: '60%', height: 28, marginTop: 8 }} />
          </div>
        ))}
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

function DashboardContent({
  data,
  onOpenRun,
  routable,
  total,
}: {
  data: DashboardData
  onOpenRun?: (runID: string) => void
  routable: number | null
  total: number | null
}) {
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

      <UsageFacts data={data} routable={routable} total={total} />

      {data.hasData ? <Breakdowns data={data} onSelectModel={openDrawer} onOpenRun={onOpenRun} /> : <EmptyState />}

      <CalibrationLeaderboard rows={data.calibration} />

      <ModelDetailDrawer row={drawerRow} open={drawerOpen} onClose={closeDrawer} />
    </div>
  )
}

/**
 * One row of facts, each a plain number with one line under it. These were
 * three cards with two ring gauges and a sparkline, which gave today's spend,
 * the figure people open this view for, the same weight as a chart legend.
 */
export function UsageFacts({
  data,
  routable,
  total,
}: {
  data: DashboardData
  routable: number | null
  total: number | null
}) {
  const { spend, governor, trust, hasData } = data
  const mode = governor.effectiveMode || governor.mode
  const headroom = governor.known ? contextHeadroom(governor) : null
  return (
    <dl className="factrow">
      <div className="fact">
        <dt className="fact__label">Spent today</dt>
        <dd className="fact__value">{hasData ? usd(spend.todayUsd) : 'none yet'}</dd>
        {hasData && <dd className="fact__sub">{usd(spend.allTimeUsd)} all time</dd>}
      </div>
      <div className="fact">
        <dt className="fact__label">Requests today</dt>
        <dd className="fact__value">{spend.todayCalls}</dd>
        {/* Provenance next to the figure: how much of the cost is measured
            rather than estimated from the length of the text. */}
        {hasData && (
          <dd className="fact__sub">
            {pct(spend.tokensActualPct)} of tokens counted by the provider
          </dd>
        )}
      </div>
      <div className="fact">
        <dt className="fact__label">Models ready</dt>
        <dd className="fact__value">
          {routable !== null && total !== null ? `${routable} of ${total}` : 'not read yet'}
        </dd>
      </div>
      <div className="fact">
        <dt className="fact__label">Context used</dt>
        {/* Not 0%. Nothing has reported usage, and 0% would read as headroom. */}
        <dd className="fact__value">{governor.known ? `${Math.round(governor.pct)}%` : 'unknown'}</dd>
        <dd className={governor.known ? `fact__sub status--${GOV_STATUS[govBand(governor.pct)]}` : 'fact__sub'}>
          {governor.known
            ? `${contextModeText(mode)}${headroom !== null ? `, about ${headroom} update${headroom === 1 ? '' : 's'} of room` : ''}`
            : 'nothing has reported it'}
        </dd>
      </div>
      <div className="fact">
        <dt className="fact__label">Checked answers</dt>
        <dd className="fact__value">{trust.runs}</dd>
        <dd className="fact__sub">{checkNote(trust)}</dd>
      </div>
    </dl>
  )
}

/** What the consensus checks bought, in a sentence. */
function checkNote(t: TrustPanel): ReactNode {
  if (t.runs === 0) {
    return (
      <>
        try <code>hyctl dispatch --confidence 0.95</code>
      </>
    )
  }
  if (t.meanSamples <= 0 || t.fixedSwarmN <= 0) return null
  return `asked ${t.meanSamples.toFixed(1)} models on average, not a fixed ${t.fixedSwarmN}`
}

function Breakdowns({
  data,
  onSelectModel,
  onOpenRun,
}: {
  data: DashboardData
  onSelectModel: (row: Breakdown) => void
  onOpenRun?: (runID: string) => void
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
        <RankedBars title="By model" hint="Select a row for its detail." rows={data.byModel} onSelect={onSelectModel} />
        <RankedBars title="By routing level (1 = most capable)" rows={data.byTier} />
      </div>
      <RecentTable rows={data.recent} onOpenRun={onOpenRun} />
    </>
  )
}

/** A ranked horizontal bar list: name, a proportional bar, and the exact
 * dollar amount, replaces the plain by-model/by-tier tables. Passing
 * `onSelect` turns each row into a button that opens the drill-down drawer
 * (used for "By model" only, one list is enough to prove the pattern out). */
function RankedBars({
  title,
  hint,
  rows,
  onSelect,
}: {
  title: string
  hint?: string
  rows: Breakdown[] | null
  onSelect?: (row: Breakdown) => void
}) {
  if (!rows || rows.length === 0) return null
  const max = Math.max(...rows.map((r) => r.costUsd))
  return (
    <section>
      <div className="section__head">
        <h2 className="section__title">{title}</h2>
        {hint && <span className="section__hint">{hint}</span>}
      </div>
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
 * Which sources actually earn their stated confidence, one row per
 * (source, domain), sorted by D descending (the order internal/trust's
 * Calibrator.Report already returns, same as `hyctl trust calibration`).
 * Bars are on an absolute nat scale, never share-of-max (#593): "who to
 * trust" has to draw a weak field as weak, and the strongest of three coin
 * flips is still a coin flip. Independent of `hasData`: calibration history
 * comes from `hyctl trust record`, not the cost log.
 */
export function CalibrationLeaderboard({ rows }: { rows: CalibrationRow[] }) {
  if (rows.length === 0) {
    return (
      <section>
        <h2 className="section__title">Which models earned their answers</h2>
        <div className="empty" style={{ marginTop: 0 }}>
          <p className="empty__title">No checked answers yet</p>
          <p>
            This fills in as answers are checked. You can also record outcomes with{' '}
            <code>hyctl trust record</code>.
          </p>
        </div>
      </section>
    )
  }

  return (
    <section>
      <h2 className="section__title">Which models earned their answers</h2>
      <div className="table__wrap">
        <div className="rank">
          {rows.map((r) => (
            <CalibrationBar key={`${r.source}\u0000${r.domain}`} row={r} />
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
          <span className="cal-legend__scale">full bar = one verdict enough for 95% on its own</span>
        </div>
      </div>
    </section>
  )
}

/** A bar's width as a share of its track, clamped: a D past the scale's top
 *  draws a full bar, never one wider than the track. */
export function calBarWidth(widthPct: number): string {
  return `${Math.min(100, Math.max(0, widthPct))}%`
}

function CalibrationBar({ row }: { row: CalibrationRow }) {
  const kind = sourceKind(row.source)
  const strength = calibrationStrength(row.d, row.n)
  return (
    <div className="cal__row">
      <span className="cal__name" title={row.source}>
        {sourceLabel(row.source)}
        <span className="cal__domain">{row.domain}</span>
      </span>
      <span className="cal__track">
        <span
          className={`cal__fill cal__fill--${kind} cal__fill--${strength}`}
          style={{ width: calBarWidth(calibrationWidthPct(row.d, row.n)) }}
        />
      </span>
      <span className="cal__value" title={`Se ${row.se.toFixed(2)}, Sp ${row.sp.toFixed(2)}, n=${row.n}`}>
        <span className={`cal__strength cal__strength--${strength}`}>{calibrationLabel(strength)}</span>
        {row.d.toFixed(2)}
      </span>
    </div>
  )
}

/** Slide-in detail panel for a "By model" row, the exact Breakdown fields
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
            <div className="drawer__title">By model</div>
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

/**
 * The last few requests, each one a way into the run that produced it.
 *
 * These rows used to be inert with a bare run id in the last column, which is
 * why the panel read as decoration: nothing here could be acted on, and the
 * one identifier shown led nowhere.
 */
export function RecentTable({
  rows,
  onOpenRun,
}: {
  rows: RecentCall[] | null
  onOpenRun?: (runID: string) => void
}) {
  if (!rows || rows.length === 0) return null
  const max = Math.max(...rows.map((r) => r.costUsd))
  return (
    <section>
      <div className="section__head">
        <h2 className="section__title">Recent requests</h2>
        {onOpenRun && (
          <span className="section__hint">Open one to see what it was asked and what it did.</span>
        )}
      </div>
      <div className="table__wrap">
        <table className="table">
          <thead>
            <tr>
              <th>Time</th>
              <th>Model</th>
              <th className="num">Level</th>
              <th className="num">Time taken</th>
              <th className="num">Cost</th>
              <th><span className="visually-hidden">Open</span></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r, i) => {
              const openable = !!(onOpenRun && r.runId)
              return (
                <tr
                  key={`${r.ts}-${i}`}
                  className={openable ? 'row--open' : undefined}
                  onClick={openable ? () => onOpenRun!(r.runId) : undefined}
                >
                  <td className="mono">{clockTime(r.ts)}</td>
                  <td>{r.model}</td>
                  <td className="num">{r.tier === 0 ? '—' : r.tier}</td>
                  <td className="num">{ms(r.wallMs)}</td>
                  <td className={`num cost--${costBand(r.costUsd, max)}`}>{usdExact(r.costUsd)}</td>
                  {/* A request with no run id has no run to open, say so rather
                      than rendering a control that does nothing. */}
                  {/* The row stays clickable; the keyboard gets a real button, since a
                      row posing as one hid the table's cells behind a label (axe). */}
                  <td className="mono">
                    {openable ? (
                      <button
                        className="row__open"
                        aria-label={`Open ${r.runId}`}
                        onClick={(e) => {
                          e.stopPropagation()
                          onOpenRun!(r.runId)
                        }}
                      >
                        open →
                      </button>
                    ) : (
                      '—'
                    )}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </section>
  )
}

function EmptyState() {
  return (
    <div className="empty" style={{ marginTop: 26 }}>
      <HydraMark className="empty__mark" decorative />
      <p className="empty__title">No requests yet</p>
      <p>Ask something in Chat and it will appear here.</p>
      <p className="empty__alt">
        From a terminal: <code>hyctl dispatch "…"</code>
      </p>
    </div>
  )
}
