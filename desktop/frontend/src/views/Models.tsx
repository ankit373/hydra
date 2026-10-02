import { useCallback, useEffect, useMemo, useState } from 'react'
import { GetDashboard, GetModels } from '../bindings'
import type { CalibrationRow, Head, HeadPanel, Model, ModelRegistry } from '../types'
import { contextWindow, sourceLabel, usdExact } from '../format'
import { PageHeader } from './PageHeader'
import { Tabs } from './Tabs'
import { ErrorState } from './ErrorState'

/** Retrospective, like the other reference views. Mirrors App's DASHBOARD_MS. */
const SLOW_MS = 5000

type Tab = 'all' | 'routable' | 'local' | 'off'
type Sort = 'tier' | 'name' | 'used' | 'context'

/** A registry model joined to the head that can actually drive it. */
interface Row {
  model: Model
  pool: string
  /** True only when the quota has something to contend against: a one-member
   *  pool is flagged shared in models.yaml and saying so would mislead. */
  shared: boolean
  head?: Head
  reach: 'on' | 'off' | 'unknown'
  /** Why it is or is not reachable, in words. Derived once, because the dot's
   *  tooltip and the scorecard's line must never disagree. */
  reachText: string
  calls: number
  costUsd: number
}

/**
 * What this machine can route to, and how well each one has actually done.
 *
 * Laid out as a catalog (filters · list · record) rather than a bare grouped
 * list, which is the shape every model directory in the field has converged
 * on and the shape this data was already rich enough to fill (#1060).
 *
 * `heads` is a prop rather than its own read: the view used to call GetHeads
 * on a 5s tick, and GetHeads is probe.Run, a full machine scan.
 */
export function Models({ heads }: { heads: HeadPanel | null }) {
  const [reg, setReg] = useState<ModelRegistry | null>(null)
  // The view used to return null on a failed read, so a dead backend rendered
  // an empty body: no header, no error, nothing at all (#1100).
  const [regError, setRegError] = useState<string | null>(null)
  const [cal, setCal] = useState<CalibrationRow[]>([])
  const [selected, setSelected] = useState<string>('')
  const [q, setQ] = useState('')
  const [tab, setTab] = useState<Tab>('all')
  const [sort, setSort] = useState<Sort>('tier')
  const [providers, setProviders] = useState<Set<string>>(new Set())

  const load = useCallback(() => {
    void GetModels()
      .then((r) => {
        setReg(r)
        setRegError(null)
      })
      .catch((e) => setRegError(e instanceof Error ? e.message : String(e)))
    // Calibration only decorates the scorecard, so its failure is not the
    // view's: the catalog is still worth rendering without it.
    void GetDashboard()
      .then((d) => setCal(d.calibration ?? []))
      .catch(() => {})
  }, [])

  useEffect(() => {
    load()
    const t = setInterval(load, SLOW_MS)
    return () => clearInterval(t)
  }, [load])

  const rows = useMemo<Row[]>(() => {
    const hs = heads?.heads ?? null
    return (reg?.pools ?? []).flatMap((p) =>
      p.models.map((m) => {
        const head = hs?.find((h) => h.id === m.id)
        // Observed spend is recorded per pool, not per model, so it is
        // attributed to the pool and shown as the pool's, never split.
        return {
          model: m,
          pool: p.name,
          shared: p.shared && p.models.length > 1,
          head,
          reach: reachClass(m, hs),
          reachText: reachText(m, hs),
          calls: p.observedCalls,
          costUsd: p.observedCostUsd,
        } as Row
      }),
    )
  }, [reg, heads])

  const counts = useMemo(
    () => ({
      all: rows.length,
      routable: rows.filter((r) => r.reach === 'on').length,
      local: rows.filter((r) => r.head?.localOnly).length,
      off: rows.filter((r) => r.reach === 'off').length,
    }),
    [rows],
  )

  const allProviders = useMemo(
    () => [...new Set(rows.map((r) => r.model.provider).filter(Boolean))].sort(),
    [rows],
  )

  const shown = useMemo(() => {
    const needle = q.trim().toLowerCase()
    const out = rows.filter((r) => {
      if (tab === 'routable' && r.reach !== 'on') return false
      if (tab === 'local' && !r.head?.localOnly) return false
      if (tab === 'off' && r.reach !== 'off') return false
      if (providers.size > 0 && !providers.has(r.model.provider)) return false
      if (!needle) return true
      return (
        r.model.id.toLowerCase().includes(needle) ||
        (r.model.name || '').toLowerCase().includes(needle) ||
        r.model.provider.toLowerCase().includes(needle)
      )
    })
    const by: Record<Sort, (a: Row, b: Row) => number> = {
      // Tier is a price band and ties are common, so every comparator falls
      // through to the id: a non-total order re-shuffles on each render.
      tier: (a, b) => a.model.tier - b.model.tier || a.model.id.localeCompare(b.model.id),
      name: (a, b) => label(a.model).localeCompare(label(b.model)),
      used: (a, b) => b.calls - a.calls || a.model.id.localeCompare(b.model.id),
      context: (a, b) =>
        b.model.contextWindow - a.model.contextWindow || a.model.id.localeCompare(b.model.id),
    }
    return [...out].sort(by[sort])
  }, [rows, q, tab, sort, providers])

  const current = shown.find((r) => r.model.id === selected) ?? shown[0]

  const toggleProvider = (p: string) =>
    setProviders((s) => {
      const next = new Set(s)
      next.has(p) ? next.delete(p) : next.add(p)
      return next
    })

  if (regError && !reg) {
    return (
      <ErrorState
        title="Models"
        what="the model registry"
        detail={regError}
        onRetry={load}
      />
    )
  }

  if (!reg) return null

  if (!reg.found) {
    return (
      <>
        <PageHeader title="Models" subtitle="What this machine can route to." />
        <div className="empty">
          <p className="empty__title">Couldn't read the model registry</p>
          <p>{reg.error || 'models.yaml could not be parsed.'}</p>
        </div>
      </>
    )
  }

  return (
    <>
      <PageHeader
        title="Models"
        subtitle="What this machine can route to, and how well each one has actually done."
        provenance={
          heads
            ? `${heads.routable} of ${heads.heads.length} discovered heads routable · declared by models.yaml, reachability by probe`
            : 'probing the machine for reachable heads…'
        }
        toolbar={
          <>
            <input
              className="tb__search"
              value={q}
              placeholder="Search models…"
              aria-label="Search models"
              onChange={(e) => setQ(e.target.value)}
            />
            <select
              className="tb__select"
              value={sort}
              aria-label="Sort by"
              onChange={(e) => setSort(e.target.value as Sort)}
            >
              <option value="tier">Cheapest tier first</option>
              <option value="name">Name</option>
              <option value="used">Most used</option>
              <option value="context">Largest context</option>
            </select>
            <span className="tb__count">
              {shown.length} of {rows.length}
            </span>
          </>
        }
      />

      <Tabs
        label="Filter models by reachability"
        panelID="models-list"
        current={tab}
        onSelect={setTab}
        tabs={[
          { id: 'all', label: 'All', count: counts.all },
          { id: 'routable', label: 'Routable', count: counts.routable },
          { id: 'local', label: 'Local', count: counts.local },
          { id: 'off', label: 'Unreachable', count: counts.off },
        ]}
      />

      <div className="catalog">
        <aside className="filters" aria-label="Filters">
          <div className="filters__group">
            <div className="filters__title">Provider</div>
            {allProviders.map((p) => (
              <label key={p} className="filters__check">
                <input
                  type="checkbox"
                  checked={providers.has(p)}
                  onChange={() => toggleProvider(p)}
                />
                <span>{p}</span>
                <span className="filters__n">
                  {rows.filter((r) => r.model.provider === p).length}
                </span>
              </label>
            ))}
            {providers.size > 0 && (
              <button className="filters__clear" onClick={() => setProviders(new Set())}>
                Clear
              </button>
            )}
          </div>
        </aside>

        <div className="catalog__list" id="models-list" role="tabpanel">
          {shown.length === 0 && (
            <p className="catalog__empty">
              No model matches. {q && <>Try clearing the search.</>}
            </p>
          )}
          {shown.map((r) => (
            <ModelRow
              key={r.model.id}
              row={r}
              active={current?.model.id === r.model.id}
              onSelect={() => setSelected(r.model.id)}
            />
          ))}
        </div>

        {current && <Scorecard row={current} cal={cal} />}
      </div>
    </>
  )
}

/** One catalog row: identity, what it is for, and what it costs. */
function ModelRow({ row, active, onSelect }: { row: Row; active: boolean; onSelect: () => void }) {
  const { model: m, head } = row
  return (
    <button className="mcard" aria-current={active ? 'true' : undefined} onClick={onSelect}>
      <span className="mcard__top">
        <span className={`mcard__dot mcard__dot--${row.reach}`} title={row.reachText} />
        <span className="mcard__name">{label(m)}</span>
        {head?.localOnly && <span className="tag tag--local">local</span>}
        {row.shared && <span className="tag tag--shared">shared quota</span>}
        {row.reach === 'off' && <span className="tag tag--off">unreachable</span>}
        <span className="mcard__spacer" />
        <span className="mcard__tier">T{m.tier}</span>
        <span className={`mcard__price mcard__price--${priceBand(m.tier)}`}>
          {priceMark(m.tier)}
        </span>
      </span>
      <span className="mcard__id">{m.id}</span>
      <span className="mcard__meta">
        <span>{m.provider}</span>
        <span>{m.contextWindow > 0 ? `${contextWindow(m.contextWindow)} context` : 'context unknown'}</span>
        <span>complexity {complexity(m)}</span>
        {m.speed && <span>{m.speed.replace(/_/g, ' ')}</span>}
        {row.calls > 0 && (
          <span
            className="mcard__used"
            title="What Hydra logged against this quota. Not a reading of the provider's remaining balance."
          >
            {poolLabel(row.pool)} &middot; {row.calls} requests &middot;{' '}
            {/* usdExact renders 0 as an em-dash, which would read "— logged". */}
            {row.costUsd > 0 ? `${usdExact(row.costUsd)} logged` : 'no cost'}
          </span>
        )}
      </span>
    </button>
  )
}

/**
 * One model's record. Capability is what the registry claims; the scorecard
 * below it is what was measured, and it never shows a score without the
 * sample count that decides whether the score means anything (#593).
 */
function Scorecard({ row, cal }: { row: Row; cal: CalibrationRow[] }) {
  const { model } = row
  const rows = cal.filter((r) => matches(r.source, model))

  return (
    <aside className="scard">
      <div className="scard__name">{label(model)}</div>
      <div className="scard__sub">
        {model.provider} &middot; tier <span className="scard__hl">T{model.tier}</span>
      </div>

      <div className="scard__rule" />
      {/* First, because it decides whether anything below is actionable. */}
      <Line k="Reachable now" v={row.reachText} />
      <Line k="Handles complexity" v={complexity(model)} />
      <Line k="Speed" v={model.speed ? model.speed.replace(/_/g, ' ') : '—'} />
      <Line k="Accuracy claimed" v={model.accuracy ? model.accuracy.replace(/_/g, ' ') : '—'} />
      <Line k="Context window" v={contextWindow(model.contextWindow)} />
      <Line k="Quota" v={poolLabel(row.pool)} />
      {row.costUsd > 0 && <Line k="Logged on quota" v={usdExact(row.costUsd)} />}

      <div className="scard__rule" />
      <div className="scard__lbl">Measured record</div>
      {rows.length === 0 ? (
        <p className="scard__none">
          Nothing measured yet. This is absence of evidence, not a low score, record outcomes
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

function label(m: Model): string {
  return m.name || m.id
}

function complexity(m: Model): string {
  if (m.complexityMax <= 0) return '—'
  return `${m.complexityMin}-${m.complexityMax}`
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

/**
 * Whether this model is reachable right now, as opposed to declared.
 *
 * The dot used to come from models.yaml's `enabled` flag, which CLAUDE.md
 * calls an install-specific default, so a head with no API key, or an Ollama
 * model whose server is down, rendered as on.
 *
 * A null head list is "nothing has looked yet", which is not the same as
 * "nothing matched": a dot either way before the first probe would be a guess.
 */
function headFor(m: Model, heads: Head[] | null): Head | undefined {
  return heads?.find((h) => h.id === m.id)
}

export function reachClass(m: Model, heads: Head[] | null): 'on' | 'off' | 'unknown' {
  if (!heads) return 'unknown'
  return headFor(m, heads)?.routable ? 'on' : 'off'
}

export function reachText(m: Model, heads: Head[] | null): string {
  if (!heads) return 'checking…'
  const h = headFor(m, heads)
  if (h) return h.routable ? 'yes' : h.reason || 'no'
  if (!m.enabled) return 'no, switched off in models.yaml'
  return 'no, the last scan did not find it'
}
