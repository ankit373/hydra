import { useEffect, useState } from 'react'
import { GetUpdateStatus, TriggerUpgrade } from '../bindings'
import type { UpdateStatus } from '../types'

/**
 * GetUpdateStatus is itself cached for 24h on the Go side (internal/update's
 * on-disk cache), so re-checking hourly costs nothing extra over the network
 *, it just catches a release cut sometime after the app was opened, without
 * needing a restart to notice.
 */
const RECHECK_MS = 60 * 60 * 1000

type Phase = 'idle' | 'upgrading' | 'done' | 'failed'

/**
 * Lives in the rail footer next to the version string. Renders nothing when
 * already current, the common case, so there is zero visual noise until an
 * update actually exists.
 *
 * "Upgrade now" runs install-app.sh as a subprocess (same script the docs
 * point a user at for a fresh install) rather than replacing this running
 * process's own binary, the app is unsigned, so a real in-place self-update
 * is out of scope; see desktop/api/update.go's TriggerUpgrade for the full
 * reasoning. The result is a new .app bundle on disk that takes effect the
 * next time the app is opened, which is why success says "quit and reopen"
 * instead of doing that automatically.
 */
export function UpdateNotice() {
  const [status, setStatus] = useState<UpdateStatus | null>(null)
  const [open, setOpen] = useState(false)
  const [phase, setPhase] = useState<Phase>('idle')
  const [output, setOutput] = useState('')

  useEffect(() => {
    const check = () =>
      GetUpdateStatus()
        .then(setStatus)
        .catch(() => {
          /* No badge beats a broken window over a failed check. */
        })
    check()
    const t = setInterval(check, RECHECK_MS)
    return () => clearInterval(t)
  }, [])

  if (!status?.available) return null

  async function upgrade() {
    setPhase('upgrading')
    try {
      const r = await TriggerUpgrade()
      setOutput(r.output)
      setPhase(r.ok ? 'done' : 'failed')
    } catch (e) {
      setOutput(e instanceof Error ? e.message : String(e))
      setPhase('failed')
    }
  }

  return (
    <div className="update-notice">
      <button
        className="update-notice__badge"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
      >
        ↑ {status.latest} available
      </button>
      {open && (
        <div className="update-notice__panel">
          {phase === 'idle' && (
            <>
              <p className="update-notice__hint">
                Downloads and installs the latest release, same as install-app.sh.
              </p>
              <button className="update-notice__cta" onClick={() => void upgrade()}>
                Upgrade now
              </button>
            </>
          )}
          {phase === 'upgrading' && <p className="update-notice__hint">Upgrading…</p>}
          {phase === 'done' && (
            <p className="update-notice__hint">
              Updated to {status.latest}. Quit and reopen Hydra to use it.
            </p>
          )}
          {phase === 'failed' && (
            <>
              <p className="update-notice__error">Upgrade failed.</p>
              <pre className="update-notice__log">{output}</pre>
            </>
          )}
        </div>
      )}
    </div>
  )
}
