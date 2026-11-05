import { useEffect, useState } from 'react'
import { CheckHyctl, InstallHyctl } from '../bindings'
import type { HyctlStatus, InstallResult } from '../types'

/**
 * The optional command-line tool, for people who also want Hydra in a
 * terminal. The app runs Hydra itself, so nothing here is needed to use it;
 * that is why this lives in Settings rather than in front of Chat (#1340).
 */
export function CliTool() {
  const [status, setStatus] = useState<HyctlStatus | null>(null)
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<InstallResult | null>(null)

  useEffect(() => {
    CheckHyctl()
      .then(setStatus)
      .catch(() => {
        /* Optional and informational: a failed check leaves the section out. */
      })
  }, [])

  if (!status) return null

  async function install() {
    setBusy(true)
    setResult(null)
    try {
      const r = await InstallHyctl()
      setResult(r)
      if (r.ok) setStatus({ ...status!, found: true, version: r.version, versionError: r.versionError })
    } finally {
      setBusy(false)
    }
  }

  return (
    <section className="set__group" aria-labelledby="cli-title">
      <h2 className="set__title" id="cli-title">
        Command-line tool (optional)
      </h2>
      <p className="set__note">
        For developers who also want Hydra in a terminal, as <code>hyctl</code>. The app does not need it.
      </p>
      <div className="set__rows">
        <div className="set__row">
          <span className="set__label">
            {status.found ? `Installed${status.version ? `: ${status.version}` : ''}` : 'Not installed'}
            {status.found && status.path && <span className="set__detail">{status.path}</span>}
            {status.found && status.versionError && (
              <span className="set__detail">Its version could not be read: {status.versionError}</span>
            )}
            {!status.found && !status.supported && (
              <span className="set__detail">
                On Windows, install it from the Hydra download page or with <code>npm install -g hyctl</code>.
              </span>
            )}
          </span>
          {!status.found && status.supported && (
            <button className="pagehead__btn" onClick={() => void install()} disabled={busy}>
              {busy ? 'Installing…' : 'Install'}
            </button>
          )}
        </div>
      </div>
      {result && !result.ok && (
        <p className="setup__error" role="alert">
          {result.error || 'Install failed for an unknown reason.'}
        </p>
      )}
      {result?.log && <pre className="setup__log">{result.log}</pre>}
    </section>
  )
}
