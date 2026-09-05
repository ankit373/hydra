import { useEffect, useRef, useState } from 'react'
import { GetModels } from '../bindings'
import type { Model, ModelRegistry } from '../types'

/**
 * The composer's model control.
 *
 * Grouped by token pool rather than by vendor because a pool is a *shared
 * quota*: opus-thinking and sonnet-thinking both draw from agy_claude, so
 * choosing one spends what the other will need. That consequence is the thing
 * worth knowing at the moment of choosing, and no other surface says it.
 *
 * A choice is expressed as a tier, since every registry model declares one.
 * It is a starting point, not a guarantee, the governor can still downgrade
 * and fallback can still move off it, so the label says "start with", not
 * "use".
 */
export function ModelPicker({
  tier,
  onPick,
  disabled,
}: {
  /** Empty means auto-route. */
  tier: string
  onPick: (tier: string) => void
  disabled?: boolean
}) {
  const [reg, setReg] = useState<ModelRegistry | null>(null)
  const [open, setOpen] = useState(false)
  const wrapRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    GetModels().then(setReg).catch(() => {
      /* Degrades to auto-route only; a missing registry must not block sending. */
    })
  }, [])

  // Dismiss on outside click and on Escape, a popover that can only be closed
  // by re-clicking its own trigger traps the keyboard.
  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (!wrapRef.current?.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    addEventListener('mousedown', onDown)
    addEventListener('keydown', onKey)
    return () => {
      removeEventListener('mousedown', onDown)
      removeEventListener('keydown', onKey)
    }
  }, [open])

  const all = reg?.pools.flatMap((p) => p.models) ?? []
  const picked = all.find((m) => String(m.tier) === tier)

  return (
    <div className="picker" ref={wrapRef}>
      {open && reg && (
        <div className="picker__pop" role="dialog" aria-label="Choose a model">
          <div className="picker__head">
            <span className="picker__title">Start this with</span>
            <span className="picker__esc">esc</span>
          </div>
          <div className="picker__body">
            <button
              className="picker__auto"
              aria-checked={tier === ''}
              role="radio"
              onClick={() => {
                onPick('')
                setOpen(false)
              }}
            >
              <span>
                <span className="picker__autoT">Auto-route</span>
                <span className="picker__autoS">
                  Hydra reads the task's complexity and picks the cheapest model that clears it
                </span>
              </span>
              {tier === '' && <span className="picker__tick">✓</span>}
            </button>

            {reg.pools.map((p) => (
              <div className="picker__pool" key={p.name}>
                <div className="picker__poolHead">
                  <span className="picker__poolName">{poolLabel(p.name)}</span>
                  {p.shared && <span className="picker__shared">shared</span>}
                  {p.observedCalls > 0 && (
                    <span className="picker__poolSpend" title="What Hydra logged against this pool, not a provider quota reading">
                      {p.observedCalls} calls · ${p.observedCostUsd.toFixed(2)}
                    </span>
                  )}
                </div>
                {p.models.map((m) => (
                  <button
                    key={m.id}
                    className="picker__m"
                    role="radio"
                    aria-checked={String(m.tier) === tier}
                    disabled={!m.enabled}
                    onClick={() => {
                      onPick(String(m.tier))
                      setOpen(false)
                    }}
                  >
                    <span className="picker__mTop">
                      <span className="picker__mName">{m.name || m.id}</span>
                      <span className="picker__mTier">T{m.tier}</span>
                      {!m.enabled && <span className="picker__off">off</span>}
                    </span>
                    {String(m.tier) === tier && <span className="picker__tick">✓</span>}
                    <span className="picker__mSpec">{spec(m, p.shared, p.models.length)}</span>
                  </button>
                ))}
              </div>
            ))}
          </div>
        </div>
      )}

      <button
        className="picker__btn"
        disabled={disabled}
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        <span className="picker__dot" />
        <span className="picker__btnName">{picked ? picked.name || picked.id : 'Auto-route'}</span>
        {picked && <span className="picker__btnTier">T{picked.tier}</span>}
        <span className="picker__caret">▲</span>
      </button>
    </div>
  )
}

/**
 * The one line under each model. Complexity leads because it is the honest
 * answer to "how hard can this one think", Hydra has no thinking-depth dial,
 * so depth is which model you pick.
 */
function spec(m: Model, shared: boolean, siblings: number): string {
  const parts: string[] = []
  if (m.complexityMax > 0) parts.push(`complexity ${m.complexityMin}-${m.complexityMax}`)
  if (m.speed) parts.push(m.speed.replace(/_/g, ' '))
  if (m.contextWindow > 0) parts.push(`${ctx(m.contextWindow)} ctx`)
  // Only worth saying when there is actually another member to starve.
  if (shared && siblings > 1) parts.push('shares this quota')
  return parts.join(' · ')
}

/** 1000000 → "1M", not "1000k". */
function ctx(tokens: number): string {
  if (tokens >= 1_000_000) {
    const m = tokens / 1_000_000
    return `${Number.isInteger(m) ? m : m.toFixed(1)}M`
  }
  return `${Math.round(tokens / 1000)}k`
}

/** agy_claude → "Claude". The raw pool keys are config, not labels. */
function poolLabel(name: string): string {
  if (name === 'unpooled') return 'No shared quota'
  return name
    .replace(/^agy_/, '')
    .replace(/^local_/, '')
    .replace(/_/g, ' ')
    .replace(/\b\w/g, (c) => c.toUpperCase())
}
