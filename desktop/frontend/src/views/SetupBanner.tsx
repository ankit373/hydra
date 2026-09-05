import { useState } from 'react'
import { InstallHyctl } from '../bindings'
import type { HyctlStatus, InstallResult } from '../types'

/**
 * The one-time first-run prompt for #383: the desktop app can be downloaded
 * on its own (install-app.sh), which leaves a real gap when hyctl was never
 * separately installed, every other view already renders safely with zero
 * ~/.hydra data, but there is nothing to route to at all.
 *
 * Invisible on every machine that already has hyctl working, which is the
 * common case, App only renders this when CheckHyctl says Found is false.
 */
export function SetupBanner({
  status,
  onChanged,
}: {
  status: HyctlStatus
  onChanged: (next: HyctlStatus) => void
}) {
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<InstallResult | null>(null)
  const [dismissed, setDismissed] = useState(false)

  if (dismissed) return null

  async function install() {
    setBusy(true)
    setResult(null)
    try {
      const r = await InstallHyctl()
      setResult(r)
      if (r.ok) {
        onChanged({
          found: true,
          version: r.version,
          versionError: r.versionError,
          supported: status.supported,
        })
      }
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="setup">
      <div className="setup__head">
        <span className="setup__title">
          {result?.ok ? 'hyctl installed' : 'hyctl is not set up on this machine'}
        </span>
        <button className="setup__dismiss" onClick={() => setDismissed(true)} aria-label="Dismiss">
          ×
        </button>
      </div>

      {!result?.ok && (
        <p className="setup__body">
          Hydra routes work to <code>hyctl</code>, but it isn't on this machine's PATH yet. Install
          it and this message won't come back.
        </p>
      )}

      {status.supported ? (
        <button className="setup__install" onClick={() => void install()} disabled={busy || result?.ok}>
          {busy ? 'Installing…' : result?.ok ? `Installed ${result.version ?? ''}`.trim() : 'Install hyctl'}
        </button>
      ) : (
        <p className="setup__body">
          Automatic setup covers macOS and Linux. On Windows, run{' '}
          <code>irm https://raw.githubusercontent.com/ankit373/hydra/main/install.ps1 | iex</code> or{' '}
          <code>npm install -g hyctl</code>, then relaunch Hydra.
        </p>
      )}

      {result && !result.ok && (
        <p className="setup__error">{result.error || 'Install failed for an unknown reason.'}</p>
      )}

      {/* The install worked; only the version read did not. Saying which beats
          a button that reads "Installed" with a blank where a version goes. */}
      {result?.ok && result.versionError && (
        <p className="setup__body">Installed, but its version could not be read: {result.versionError}</p>
      )}

      {result?.log && (
        <pre className="setup__log">{result.log}</pre>
      )}
    </div>
  )
}
