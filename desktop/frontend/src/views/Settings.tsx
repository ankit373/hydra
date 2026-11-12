import { useCallback, useEffect, useState } from 'react'
import { GetSettings, SaveSettings } from '../bindings'
import type { Settings as SettingsData } from '../types'
import { PageHeader } from './PageHeader'
import { ErrorState } from './ErrorState'

/**
 * What this machine is set to do, and the only place in the app that writes
 * anything. Until #1108 the app could read every number Hydra produces and
 * change nothing about it: config.toml was reachable only by hand or by
 * re-running the `hyctl init` wizard, which asks five of these questions.
 *
 * Each switch says what it costs rather than only what it is named.
 * `internal/config` already argues every one of them in a paragraph, and that
 * reasoning reached nobody who did not read the source.
 */
export function Settings() {
  const [data, setData] = useState<SettingsData | null>(null)
  const [draft, setDraft] = useState<SettingsData | null>(null)
  const [readError, setReadError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)

  const load = useCallback(() => {
    void GetSettings()
      .then((s) => {
        setData(s)
        setDraft(s)
        setReadError(null)
      })
      .catch((e) => setReadError(e instanceof Error ? e.message : String(e)))
  }, [])

  useEffect(load, [load])

  if (readError) {
    return (
      <ErrorState title="Settings" what="the config" detail={readError} onRetry={load} />
    )
  }
  if (!data || !draft) return null

  // A config that exists and does not parse is reported, never rendered as
  // defaults: showing defaults invites a save that destroys the only record of
  // what was meant.
  if (!data.readable) {
    return (
      <div className="set">
        <PageHeader title="Settings" subtitle="What this machine is set to do." />
        <div className="failure" role="alert">
          <p className="failure__title">Hydra can't read this config</p>
          <pre className="failure__detail">{data.error}</pre>
          <p className="set__note">
            Nothing here can be changed until the file parses, because saving over it would
            discard the settings it holds. Fix it at <code>{data.path}</code>, then retry.
          </p>
          <button className="failure__retry" onClick={load}>
            Try again
          </button>
        </div>
      </div>
    )
  }

  const set = <K extends keyof SettingsData>(k: K, v: SettingsData[K]) => {
    setDraft({ ...draft, [k]: v })
    setSaved(false)
  }

  const dirty = JSON.stringify(draft) !== JSON.stringify(data)

  const save = () => {
    setSaving(true)
    void SaveSettings(draft)
      .then((s) => {
        setData(s)
        // The backend answers with what it actually stored, so a value it
        // refused or normalised is reflected rather than left on screen.
        setDraft(s)
        setSaved(!s.error)
      })
      .catch((e) => setReadError(e instanceof Error ? e.message : String(e)))
      .finally(() => setSaving(false))
  }

  return (
    <div className="set">
      <PageHeader
        title="Settings"
        subtitle="What this machine is set to do."
        provenance={`${data.path}${data.exists ? '' : ' · not written yet, these are the defaults'}`}
        actions={
          <>
            {saved && !dirty && <span className="set__ok">Saved</span>}
            <button className="pagehead__btn" onClick={() => setDraft(data)} disabled={!dirty}>
              Discard
            </button>
            <button
              className="set__save"
              onClick={save}
              disabled={!dirty || saving}
            >
              {saving ? 'Saving…' : 'Save'}
            </button>
          </>
        }
      />

      {draft.error && (
        <div className="failure" role="alert">
          <p className="failure__title">That wasn't saved</p>
          <pre className="failure__detail">{draft.error}</pre>
        </div>
      )}

      <Group
        title="What leaves this machine"
        note="Both of these are about where work can go, not how much it costs."
      >
        <Toggle
          label="Route anything with PII to a local head"
          detail="When the detector fires, only heads that run on this machine may see the content."
          on={draft.piiLocalOnly}
          onChange={(v) => set('piiLocalOnly', v)}
        />
        <Toggle
          label="Refuse rather than send, when nothing local is routable"
          detail="With this off, a secret payload goes to a head that reaches the network instead of failing. Absent means on, which is the safe reading of a setting nobody chose."
          on={draft.strictEgress}
          onChange={(v) => set('strictEgress', v)}
        />
      </Group>

      <Group
        title="What gets stored"
        note="Off by default, and argued separately: these are the only trace classes with real privacy risk."
      >
        <Toggle
          label="Keep prompt and response text"
          detail="Verbatim source and prompts. Redacted before writing and bounded by the budget below, which drops the oldest packs rather than refusing."
          on={draft.capturePayloads}
          onChange={(v) => set('capturePayloads', v)}
        />
        {draft.capturePayloads && (
          <Num
            label="Payload budget"
            unit="MB"
            hint="0 uses the built-in default"
            value={draft.payloadBudgetMb}
            onChange={(v) => set('payloadBudgetMb', v)}
          />
        )}
        <Toggle
          label="Keep a vector per dispatch"
          detail="An embedding is not plaintext, but inversion attacks recover approximate text from one, so it is its own decision rather than part of the one above."
          on={draft.captureEmbeddings}
          onChange={(v) => set('captureEmbeddings', v)}
        />
        {draft.captureEmbeddings && (
          <>
            <Text
              label="Embedding model"
              hint="Normally discovered. Name one only if your Ollama reports no capabilities."
              value={draft.embedModel}
              onChange={(v) => set('embedModel', v)}
            />
            <Num
              label="Vector budget"
              unit="MB"
              hint="0 uses the built-in default"
              value={draft.embedBudgetMb}
              onChange={(v) => set('embedBudgetMb', v)}
            />
          </>
        )}
      </Group>

      <Group
        title="Answering from a previous run"
        note="The most consequential switch here. The others store what happened; this one changes what comes back."
      >
        <Toggle
          label="Serve an answer from a matching earlier question"
          detail="A near match returns a confident answer to a question nobody asked, so the cache refuses far more than it serves: the words, their order and what they refer to all have to match before similarity is even consulted."
          on={draft.cacheAnswers}
          onChange={(v) => set('cacheAnswers', v)}
        />
        {draft.cacheAnswers && (
          <>
            <Num
              label="Similarity a near match must reach"
              step={0.01}
              hint="Between 0 and 1. 0 uses the built-in default. Anything outside that range is ignored by the router rather than clamped, so it is refused here."
              value={draft.cacheThreshold}
              onChange={(v) => set('cacheThreshold', v)}
            />
            <Num
              label="Cache budget"
              unit="MB"
              hint="0 uses the built-in default"
              value={draft.cacheBudgetMb}
              onChange={(v) => set('cacheBudgetMb', v)}
            />
          </>
        )}
      </Group>

      <Group
        title="Routing"
        note="Changing the Cortex means picking from the heads this machine discovered, which is what `hyctl init` is for."
      >
        <Num
          label="How often to try a head other than the best one"
          step={0.01}
          hint="Between 0 and 1. 0 is pure argmax and changes nothing. Without some exploration, questions about what another head would have done are unidentifiable rather than merely noisy, so `hyctl trace evaluate` refuses them."
          value={draft.exploreRate}
          onChange={(v) => set('exploreRate', v)}
        />
        <Read label="Cortex" value={data.cortex || '—'} />
        <Read label="Skills" value={data.skills?.join(', ') || '—'} />
        <Read
          label="OpenRouter models"
          value={
            data.openRouterModels?.length
              ? data.openRouterModels.join(', ')
              : 'none named, so the key routes as one head'
          }
        />
      </Group>
    </div>
  )
}

function Group({
  title,
  note,
  children,
}: {
  title: string
  note: string
  children: React.ReactNode
}) {
  return (
    <section className="set__group">
      <h2 className="set__title">{title}</h2>
      <p className="set__note">{note}</p>
      <div className="set__rows">{children}</div>
    </section>
  )
}

function Toggle({
  label,
  detail,
  on,
  onChange,
}: {
  label: string
  detail: string
  on: boolean
  onChange: (v: boolean) => void
}) {
  return (
    <label className="set__row set__row--toggle">
      <input
        type="checkbox"
        className="set__check"
        checked={on}
        onChange={(e) => onChange(e.target.checked)}
      />
      <span className="set__label">
        {label}
        <span className="set__detail">{detail}</span>
      </span>
    </label>
  )
}

function Num({
  label,
  unit,
  hint,
  step,
  value,
  onChange,
}: {
  label: string
  unit?: string
  hint: string
  step?: number
  value: number
  onChange: (v: number) => void
}) {
  return (
    <div className="set__row">
      <span className="set__label">
        {label}
        <span className="set__detail">{hint}</span>
      </span>
      <span className="set__field">
        <input
          type="number"
          className="set__input"
          step={step ?? 1}
          min={0}
          value={value}
          aria-label={label}
          onChange={(e) => onChange(Number(e.target.value))}
        />
        {unit && <span className="set__unit">{unit}</span>}
      </span>
    </div>
  )
}

function Text({
  label,
  hint,
  value,
  onChange,
}: {
  label: string
  hint: string
  value: string
  onChange: (v: string) => void
}) {
  return (
    <div className="set__row">
      <span className="set__label">
        {label}
        <span className="set__detail">{hint}</span>
      </span>
      <input
        className="set__input set__input--text"
        value={value}
        aria-label={label}
        placeholder="discovered"
        onChange={(e) => onChange(e.target.value)}
      />
    </div>
  )
}

/** A fact this view reports and does not write. */
function Read({ label, value }: { label: string; value: string }) {
  return (
    <div className="set__row set__row--read">
      <span className="set__label">{label}</span>
      <span className="set__ro">{value}</span>
    </div>
  )
}
