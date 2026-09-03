import { useEffect, useMemo, useState } from 'react'
import { GetDashboard, GetModels } from '../bindings'
import type { CalibrationRow, Model, ModelRegistry, Pool } from '../types'
import { sourceLabel, usdExact } from '../format'

/** Retrospective, like the other reference views. Mirrors App's DASHBOARD_MS. */
const SLOW_MS = 5000

/**
 * What this machine can route to, and how well each one has actually done.
 *
 * The registry has shipped since #553 and until now fed nothing but the
 * composer's picker, so the app could route to twelve models and show you none
 * of them. Grouped by shared quota rather than by vendor, because the quota is
 * what a choice actually spends.
 */
export function Models() {
  const [reg, setReg] = useState<ModelRegistry | null>(null)
  const [cal, setCal] = useState<CalibrationRow[]>([])
  const [selected, setSelected] = useState<string>('')

  useEffect(() => {
    const load = () => {
      void GetModels().then(setReg).catch(() => {})
      void GetDashboard()
        .then((d) => setCal(d.calibration ?? []))
        .catch(() => {})
    }
    load()
    const t = setInterval(load, SLOW_MS)
    return () => clearInterval(t)
  }, [])

  const all = useMemo(() => reg?.pools.flatMap((p) => p.models) ?? [], [reg])
  const current = all.find((m) => m.id === selected) ?? all[0]

  if (!reg) return null

  if (!reg.found) {
    return (
      <>
        <Head />
        <div className="empty">
          <p className="empty__title">Couldn't read the model registry</p>
          <p>{reg.error || 'models.yaml could not be parsed.'}</p>
        </div>
      </>
    )
  }

  return (
    <>
      <Head />
      <div className="models">
        <div className="models__tree">
          {reg.pools.map((p) => (
            <PoolGroup
              key={p.name}
              pool={p}
              selected={current?.id ?? ''}
              onSelect={setSelected}
            />
          ))}
        </div>
        {current && <Scorecard model={current} cal={cal} />}
      </div>
    </>
  )
}

function Head() {
  return (
    <header className="view__head">
      <h1 className="view__title">Models</h1>
      <p className="view__sub">What this machine can route to, grouped by the quota each one spends.</p>
    </header>
  )
}

function PoolGroup({
  pool,
  selected,
  onSelect,
}: {
  pool: Pool
  selected: string
  onSelect: (id: string) => void
}) {
  return (
    <section className="pool-g">
      <div className="pool-g__head">
        <span className="pool-g__name">{poolLabel(pool.name)}</span>
        {pool.shared && pool.models.length > 1 && <span className="pool-g__shared">shared quota</span>}
        {pool.observedCalls > 0 && (
          <span
            className="pool-g__spend"
            title="What Hydra logged against this quota. Not a reading of the provider's remaining balance."
          >
            {pool.observedCalls} requests &middot;{' '}
            {pool.observedCostUsd > 0 ? `${usdExact(pool.observedCostUsd)} logged` : 'no cost'}
          </span>
        )}
      </div>
      {pool.models.map((m) => (
        <button
          key={m.id}
          className="mrow"
          aria-current={m.id === selected ? 'true' : undefined}
          onClick={() => onSelect(m.id)}
        >
          <span className={`mrow__dot ${m.enabled ? 'mrow__dot--on' : 'mrow__dot--off'}`} />
          <span className="mrow__name">{m.name || m.id}</span>
          <span className="mrow__tier">T{m.tier}</span>
          <span className="mrow__band">{complexity(m)}</span>
          <span className={`mrow__cost mrow__cost--${priceBand(m.tier)}`}>{priceMark(m.tier)}</span>
        </button>
      ))}
    </section>
  )
}

/**
 * One model's record. Capability is what the registry claims; the scorecard
 * below it is what was measured — and it never shows a score without the
 * sample count that decides whether the score means anything (#593).
 */
function Scorecard({ model, cal }: { model: Model; cal: CalibrationRow[] }) {
  const rows = cal.filter((r) => matches(r.source, model))

  return (
    <aside className="scard">
      <div className="scard__name">{model.name || model.id}</div>
      <div className="scard__sub">
        {model.provider} &middot; tier <span className="scard__hl">T{model.tier}</span>
        {!model.enabled && <span className="scard__off"> &middot; switched off</span>}
      </div>

      <div className="scard__rule" />
      <Line k="Handles complexity" v={complexity(model)} />
      <Line k="Speed" v={model.speed ? model.speed.replace(/_/g, ' ') : '—'} />
      <Line k="Accuracy claimed" v={model.accuracy ? model.accuracy.replace(/_/g, ' ') : '—'} />
      <Line k="Context window" v={model.contextWindow > 0 ? `${Math.round(model.contextWindow / 1000)}k` : '—'} />

      <div className="scard__rule" />
      <div className="scard__lbl">Measured record</div>
      {rows.length === 0 ? (
        <p className="scard__none">
          Nothing measured yet. This is absence of evidence, not a low score — record outcomes
          and it fills in.
        </p>
      ) : (
        rows.map((r) => (
          <div className="scard__cal" key={`${r.source} ${r.domain}`}>
            <div className="scard__calTop">
              <span className="scard__calDom">{r.domain}</span>
              <span className={`scard__calD scard__calD--${weight(r)}`}>{r.d.toFixed(2)}</span>
            </div>
            <div className="scard__calSub">
              evidence weight &middot; {r.n} {r.n === 1 ? 'outcome' : 'outcomes'}
              {r.n < 10 && <span className="scard__thin"> &middot; too few to trust</span>}
            </div>
          </div>
        ))
      )}
    </aside>
  )
}

function Line({ k, v }: { k: string; v: string }) {
  return (
    <div className="scard__line">
      <span className="scard__k">{k}</span>
      <span className="scard__v">{v}</span>
    </div>
  )
}

function complexity(m: Model): string {
  if (m.complexityMax <= 0) return '—'
  return `${m.complexityMin}–${m.complexityMax}`
}

/**
 * A model's calibration is keyed by source ("model:claude-sonnet"), which is
 * not the registry id. Match on the labelled tail so a renamed registry entry
 * does not silently drop its own history.
 */
function matches(source: string, m: Model): boolean {
  const tail = sourceLabel(source).toLowerCase()
  const id = m.id.toLowerCase()
  const name = (m.name || '').toLowerCase()
  return tail === id || id.includes(tail) || name.includes(tail)
}

/** Strong / moderate / weak, by the same thresholds the reference views use. */
function weight(r: CalibrationRow): 'strong' | 'moderate' | 'weak' | 'thin' {
  if (r.n < 10) return 'thin'
  if (r.d >= 1) return 'strong'
  if (r.d >= 0.5) return 'moderate'
  return 'weak'
}

/** Price as a shape, not a number: tiers are ordinal and rates move. */
function priceMark(tier: number): string {
  if (tier >= 10) return 'free'
  if (tier >= 7) return '$'
  if (tier >= 4) return '$$'
  return '$$$'
}
function priceBand(tier: number): 'free' | 'low' | 'mid' | 'high' {
  if (tier >= 10) return 'free'
  if (tier >= 7) return 'low'
  if (tier >= 4) return 'mid'
  return 'high'
}

/** agy_claude → "Claude". The raw keys are config, not labels. */
export function poolLabel(name: string): string {
  if (name === 'unpooled') return 'No shared quota'
  return name
    .replace(/^agy_/, '')
    .replace(/^local_/, '')
    .replace(/_/g, ' ')
    .replace(/\b\w/g, (c) => c.toUpperCase())
}
