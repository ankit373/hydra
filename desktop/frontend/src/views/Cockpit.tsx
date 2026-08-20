import { useEffect, useState } from 'react'
import { GetDashboard, GetEdits, GetModels } from '../bindings'
import type {
  CalibrationRow,
  ChatReply,
  Dashboard as DashboardData,
  Edit,
  ModelRegistry,
  Session as SessionData,
} from '../types'
import { pct, usd } from '../format'

/** Dashboard is retrospective; a slow tick is enough. Mirrors App's DASHBOARD_MS. */
const SLOW_MS = 5000

/**
 * The companion pane beside chat (#520).
 *
 * Deliberately glanceable, never readable: chips, meters and one gauge. That is
 * the answer to the split-attention objection the old chat dock's
 * collapse-on-idle behaviour was built around — a permanently visible panel is
 * only affordable if nothing in it asks to be read.
 */
export function Cockpit({
  live,
  lastReply,
  runId,
}: {
  /** The run currently in flight, if any. */
  live: SessionData | null
  /** The most recent settled reply, for when nothing is in flight. */
  lastReply?: ChatReply
  runId?: string
}) {
  const [reg, setReg] = useState<ModelRegistry | null>(null)
  const [dash, setDash] = useState<DashboardData | null>(null)
  const [edits, setEdits] = useState<Edit[]>([])

  useEffect(() => {
    GetModels().then(setReg).catch(() => {})
  }, [])

  useEffect(() => {
    const tick = () => void GetDashboard().then(setDash).catch(() => {})
    tick()
    const t = setInterval(tick, SLOW_MS)
    return () => clearInterval(t)
  }, [])

  // Files are per-run, so this follows whichever run the thread is on. Refetched
  // when the run settles, since edits land throughout a dispatch.
  useEffect(() => {
    if (!runId) {
      setEdits([])
      return
    }
    let stale = false
    const tick = () =>
      void GetEdits(runId)
        .then((e) => {
          if (!stale) setEdits(e ?? [])
        })
        .catch(() => {})
    tick()
    const t = setInterval(tick, SLOW_MS)
    return () => {
      stale = true
      clearInterval(t)
    }
  }, [runId, live?.live])

  const working = live?.live === true
  // The active head: the live run's deepest-known agent while working, else
  // whatever answered last.
  const agent = live?.agents?.[0]
  const model = working ? agent?.model || agent?.head : lastReply?.model || lastReply?.head
  const tier = working ? agent?.tier ?? 0 : lastReply?.tier ?? 0

  const pool = poolFor(reg, tier)
  const conf = live?.timeline?.reduce((c, e) => (e.confidence > 0 ? e.confidence : c), 0) ?? 0

  return (
    <aside className="cockpit" aria-label="Routing detail">
      <section className="ck">
        <div className="ck__card">
          <div className="ck__cardGrad" />
          <div className="ck__cardIn">
            <div className="ck__top">
              <span className="ck__name">{model || 'No model yet'}</span>
              {tier > 0 && <span className="ck__tier">T{tier}</span>}
              {working && <span className="ck__pulse" title="working" />}
            </div>
            {/* Neither "working…" nor the latest event: the thread already
                shows both, and repeating either just doubles the same words on
                screen. A running count is aggregate, so it adds something. */}
            <div className="ck__sub">
              {working ? stepCount(live) : model ? 'idle' : 'Ask something to start'}
            </div>

            {pool && (
              <div className="pm">
                <div className="pm__top">
                  <span className="pm__label">{poolLabel(pool.name)}</span>
                  {pool.shared && <span className="pm__shared">shared</span>}
                </div>
                {/* Observed spend, not a quota reading: Hydra cannot see the
                    provider's remaining allocation, so this says what it is. */}
                <div className="pm__val">
                  {pool.observedCalls} calls · {usd(pool.observedCostUsd)} logged
                </div>
                {pool.shared && pool.models.length > 1 && (
                  <div className="pm__with">
                    shares this quota with{' '}
                    {pool.models
                      .filter((m) => m.tier !== tier)
                      .map((m) => m.name || m.id)
                      .join(', ')}
                  </div>
                )}
              </div>
            )}
          </div>
        </div>
      </section>

      {conf > 0 && (
        <section className="ck">
          <div className="ck__head">
            <span className="ck__lbl">Confidence</span>
            <span className="ck__rule" />
          </div>
          <div className="ck__gauge">
            <Gauge value={conf} />
            <div>
              <div className="ck__gaugeNum">{pct(conf * 100, 0)}</div>
              <div className="ck__gaugeCap">reached on this run</div>
            </div>
          </div>
        </section>
      )}

      <section className="ck">
        <div className="ck__head">
          <span className="ck__lbl">Who worked, and how good they are at it</span>
          <span className="ck__rule" />
        </div>
        <ModelRows dash={dash} />
      </section>

      {edits.length > 0 && (
        <section className="ck">
          <div className="ck__head">
            <span className="ck__lbl">Files this run changed</span>
            <span className="ck__rule" />
          </div>
          {edits.slice(0, 6).map((e, i) => (
            <div className="fr" key={`${e.file}-${i}`}>
              <span className="fr__name" title={e.file}>
                {basename(e.file)}
              </span>
              <span className="fr__pm">
                <span className="add">+{e.added}</span> <span className="del">−{e.removed}</span>
              </span>
            </div>
          ))}
          {edits.length > 6 && <div className="fr__more">+{edits.length - 6} more</div>}
        </section>
      )}
    </aside>
  )
}

/**
 * Per-model rows fusing what a model did with how good it is measured to be at
 * that — usage alone is a tally, not a judgement.
 *
 * The sample size ships next to every score because trust.Stat.N excludes the
 * prior: a D on a handful of observations is close to a coin flip, and showing
 * the number bare would present it as a measurement.
 */
function ModelRows({ dash }: { dash: DashboardData | null }) {
  const cal = dash?.calibration ?? []
  const byModel = dash?.byModel ?? []

  if (cal.length === 0 && byModel.length === 0) {
    return (
      <p className="ck__empty">
        Nothing recorded yet. Calibration fills in from <code>hyctl trust record</code>.
      </p>
    )
  }

  // Usage leads the ordering (what actually ran), with calibration joined on.
  const rows = byModel.length > 0 ? byModel.map((b) => ({ key: b.key, calls: b.calls })) : []
  return (
    <div className="mr">
      {rows.slice(0, 6).map((r) => {
        const best = bestFor(cal, r.key)
        return (
          <div className="mr__row" key={r.key}>
            <div className="mr__top">
              <span className="mr__name" title={r.key}>
                {r.key}
              </span>
              <span className={`mr__d ${best ? band(best) : 'mr__d--none'}`}>
                {best ? best.d.toFixed(2) : '—'}
              </span>
            </div>
            <div className="mr__bot">
              <span className="mr__did">{r.calls} calls</span>
              <span className={`mr__n ${best && best.n < 10 ? 'mr__n--thin' : ''}`}>
                {best ? `${best.domain} · ${best.n} obs${best.n < 10 ? ' · thin' : ''}` : 'never calibrated'}
              </span>
            </div>
          </div>
        )
      })}
    </div>
  )
}

/** Strongest-measured domain for a source, or undefined when uncalibrated. */
function bestFor(cal: CalibrationRow[], key: string): CalibrationRow | undefined {
  const mine = cal.filter((c) => c.source === key || c.source.endsWith(key))
  if (mine.length === 0) return undefined
  return mine.reduce((a, b) => (b.d > a.d ? b : a))
}

// Bands, not a gradient: D is in nats and ~1 is strong discrimination, so three
// buckets say more than a continuous scale nobody can read.
function band(c: CalibrationRow): string {
  if (c.n < 10) return 'mr__d--thin'
  if (c.d >= 1) return 'mr__d--strong'
  if (c.d >= 0.5) return 'mr__d--ok'
  return 'mr__d--weak'
}

function Gauge({ value }: { value: number }) {
  const r = 21
  const circ = 2 * Math.PI * r
  return (
    <svg className="gz" width="52" height="52" viewBox="0 0 52 52" aria-hidden="true">
      <circle cx="26" cy="26" r={r} className="gz__track" />
      <circle
        cx="26"
        cy="26"
        r={r}
        className="gz__fill"
        strokeDasharray={circ}
        strokeDashoffset={circ * (1 - Math.min(1, Math.max(0, value)))}
        transform="rotate(-90 26 26)"
      />
    </svg>
  )
}

/** How far along the run is, without restating what the thread already lists. */
function stepCount(live: SessionData | null): string {
  const n = live?.timeline?.length ?? 0
  if (n === 0) return 'starting…'
  return `${n} step${n === 1 ? '' : 's'} so far`
}

function poolFor(reg: ModelRegistry | null, tier: number) {
  if (!reg || tier <= 0) return undefined
  return reg.pools.find((p) => p.models.some((m) => m.tier === tier))
}

function poolLabel(name: string): string {
  if (name === 'unpooled') return 'No shared quota'
  return name.replace(/^agy_/, '').replace(/^local_/, '').replace(/_/g, ' ')
}

function basename(p: string): string {
  const i = Math.max(p.lastIndexOf('/'), p.lastIndexOf('\\'))
  return i < 0 ? p : p.slice(i + 1)
}
