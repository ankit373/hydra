import { useEffect, useState } from 'react'
import {
  GetProviderKeys,
  RemoveProviderKey,
  SaveProviderKey,
  TestProviderKey,
  openExternal,
} from '../bindings'
import type { KeyTest, ProviderKey, ProviderKeys } from '../types'

const msg = (e: unknown) => (e instanceof Error ? e.message : String(e))

/** What a row says about its key, without ever showing the key. */
function status(k: ProviderKey): string {
  const ending = k.hint ? `, ending in ${k.hint}` : ''
  if (k.source === 'stored') return `Saved${ending}`
  if (k.source === 'environment') return `Set outside Hydra${ending}. It takes priority over a key saved here.`
  return 'Not added'
}

function testResult(t: KeyTest): string {
  if (t.ok) return `Works. ${t.model ? `${t.model} answered` : 'The provider answered'}.`
  return t.error ?? 'The test failed.'
}

/**
 * Saves a pasted key, then makes one tiny request with it, so the person
 * learns the key works here rather than on their first real question.
 */
async function saveAndTest(provider: string, key: string): Promise<{ keys: ProviderKeys; test?: KeyTest }> {
  const keys = await SaveProviderKey(provider, key)
  if (keys.error) return { keys }
  // A key exported outside Hydra is the one in use, so testing now would
  // report on it rather than on the key just pasted.
  if (keys.keys.find((k) => k.provider === provider)?.source === 'environment') {
    return {
      keys,
      test: { ok: false, accepted: false, error: 'Saved. A key set outside Hydra is used instead while it stays set, so this one was not tested.' },
    }
  }
  return { keys, test: await TestProviderKey(provider) }
}

/** The Settings section: every provider, its key's state, and add, test and remove. */
export function Providers({ onChange }: { onChange?: () => void }) {
  const [data, setData] = useState<ProviderKeys | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [editing, setEditing] = useState<string | null>(null)
  const [draft, setDraft] = useState('')
  const [busy, setBusy] = useState<string | null>(null)
  const [tests, setTests] = useState<Record<string, string>>({})

  useEffect(() => {
    GetProviderKeys().then(setData).catch((e) => setError(msg(e)))
  }, [])

  const apply = (next: ProviderKeys) => {
    setData(next)
    setError(next.error ?? null)
    onChange?.()
  }

  const save = async (provider: string) => {
    setBusy(provider)
    try {
      const { keys, test } = await saveAndTest(provider, draft)
      apply(keys)
      if (!keys.error) {
        setEditing(null)
        setDraft('')
      }
      if (test) setTests((t) => ({ ...t, [provider]: testResult(test) }))
    } catch (e) {
      setError(msg(e))
    } finally {
      setBusy(null)
    }
  }

  const test = async (provider: string) => {
    setBusy(provider)
    try {
      const r = await TestProviderKey(provider)
      setTests((t) => ({ ...t, [provider]: testResult(r) }))
    } catch (e) {
      setError(msg(e))
    } finally {
      setBusy(null)
    }
  }

  const remove = async (provider: string) => {
    setBusy(provider)
    try {
      apply(await RemoveProviderKey(provider))
      setTests((t) => ({ ...t, [provider]: '' }))
    } catch (e) {
      setError(msg(e))
    } finally {
      setBusy(null)
    }
  }

  return (
    <section className="set__group" aria-labelledby="providers-title">
      <h2 className="set__title" id="providers-title">
        AI providers
      </h2>
      <p className="set__note">
        Paste a key from any provider you use. Hydra keeps it in {data?.store ?? 'a safe place'}, shows only
        its last four characters, and sends it only to that provider.
      </p>
      {error && (
        <p className="setup__error" role="alert">
          {error}
        </p>
      )}
      <div className="set__rows">
        {data?.keys.map((k) => (
          <div className="set__row" key={k.provider}>
            <span className="set__label">
              {k.label}
              <span className="set__detail">{status(k)}</span>
              {tests[k.provider] && (
                <span className="set__detail" role="status">
                  {tests[k.provider]}
                </span>
              )}
              {editing === k.provider && (
                <span className="set__field">
                  <input
                    className="set__input set__input--text"
                    type="password"
                    autoComplete="off"
                    spellCheck={false}
                    aria-label={`${k.label} key`}
                    placeholder="Paste your key"
                    value={draft}
                    onChange={(e) => setDraft(e.target.value)}
                  />
                  <button
                    className="set__save"
                    onClick={() => void save(k.provider)}
                    disabled={busy !== null || draft.trim() === ''}
                  >
                    {busy === k.provider ? 'Checking…' : 'Save'}
                  </button>
                  <button className="pagehead__btn" onClick={() => openExternal(k.keyUrl)}>
                    Get a key
                  </button>
                </span>
              )}
            </span>
            <span className="set__field">
              {k.source !== '' && (
                <button className="pagehead__btn" onClick={() => void test(k.provider)} disabled={busy !== null}>
                  {busy === k.provider && editing !== k.provider ? 'Testing…' : 'Test'}
                </button>
              )}
              {k.saved && (
                <button className="pagehead__btn" onClick={() => void remove(k.provider)} disabled={busy !== null}>
                  Remove
                </button>
              )}
              {editing !== k.provider && (
                <button
                  className="pagehead__btn"
                  onClick={() => {
                    setEditing(k.provider)
                    setDraft('')
                  }}
                  disabled={busy !== null}
                >
                  {k.saved ? 'Replace' : 'Add key'}
                </button>
              )}
            </span>
          </div>
        ))}
      </div>
    </section>
  )
}

/**
 * The first-run form: pick a provider, paste a key, and it is saved and tried
 * in one step. onWorking fires only once the provider has answered.
 */
export function AddKey({ onWorking }: { onWorking: () => void }) {
  const [data, setData] = useState<ProviderKeys | null>(null)
  const [provider, setProvider] = useState('')
  const [key, setKey] = useState('')
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<string | null>(null)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    GetProviderKeys()
      .then(setData)
      .catch((e) => setResult(msg(e)))
  }, [])

  const chosen = data?.keys.find((k) => k.provider === provider)

  const submit = async () => {
    setBusy(true)
    setResult(null)
    try {
      const { keys, test } = await saveAndTest(provider, key)
      setData(keys)
      if (keys.error) {
        setFailed(true)
        setResult(keys.error)
      } else if (test) {
        setFailed(!test.ok)
        setResult(testResult(test))
        // A key the provider took is a working key; an account out of credit
        // is a problem the sample request will name, not a reason to stay here.
        if (test.accepted) onWorking()
      }
    } catch (e) {
      setFailed(true)
      setResult(msg(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="set__rows">
      <label className="set__row">
        <span className="set__label">
          1. Choose your provider
          <span className="set__detail">The company you have an account with.</span>
        </span>
        <select
          className="set__input set__input--text"
          value={provider}
          onChange={(e) => {
            setProvider(e.target.value)
            setResult(null)
          }}
        >
          <option value="">Choose…</option>
          {data?.keys.map((k) => (
            <option key={k.provider} value={k.provider}>
              {k.label}
            </option>
          ))}
        </select>
      </label>
      {chosen && (
        <div className="set__row">
          <span className="set__label">
            2. Get a key from {chosen.label}
            <span className="set__detail">
              Sign in on their site, create a new key, and copy it. A ChatGPT or Claude subscription does not cover this: the site may ask you to add a payment method or a few dollars of credit first.
            </span>
          </span>
          <button className="pagehead__btn" onClick={() => openExternal(chosen.keyUrl)}>
            Open {chosen.label.split(' (')[0]}
          </button>
        </div>
      )}
      {chosen && (
        <label className="set__row">
          <span className="set__label">
            3. Paste it here
            <span className="set__detail">Kept in {data?.store}. Only the last four characters are ever shown.</span>
          </span>
          <span className="set__field">
            <input
              className="set__input set__input--text"
              type="password"
              autoComplete="off"
              spellCheck={false}
              placeholder="Paste your key"
              value={key}
              onChange={(e) => setKey(e.target.value)}
            />
            <button className="set__save" onClick={() => void submit()} disabled={busy || key.trim() === ''}>
              {busy ? 'Checking…' : 'Save and check'}
            </button>
          </span>
        </label>
      )}
      {result && (
        <p className={failed ? 'setup__error' : 'set__ok'} role={failed ? 'alert' : 'status'}>
          {result}
        </p>
      )}
    </div>
  )
}
