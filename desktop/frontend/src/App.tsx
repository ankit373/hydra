import { useCallback, useEffect, useState } from 'react'
import { GetDashboard, GetVersion } from './bindings'
import type { Dashboard as DashboardData, Version } from './types'
import { Dashboard } from './views/Dashboard'

/** Dashboard is retrospective — a slow refresh is enough and costs nothing. */
const REFRESH_MS = 5000

// Fleet, Session, and Code arrive in Phases 4-6. They are listed disabled
// rather than hidden so the shell's shape is honest about where this is going,
// and rendered as "soon" rather than as empty views that look broken.
const NAV = [
  { id: 'dashboard', label: 'Dashboard', ready: true },
  { id: 'fleet', label: 'Fleet', ready: false },
  { id: 'session', label: 'Session', ready: false },
  { id: 'code', label: 'Code', ready: false },
] as const

export default function App() {
  const [data, setData] = useState<DashboardData | null>(null)
  const [version, setVersion] = useState<Version | null>(null)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    try {
      setData(await GetDashboard())
      setError(null)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [])

  useEffect(() => {
    void load()
    const t = setInterval(() => void load(), REFRESH_MS)
    return () => clearInterval(t)
  }, [load])

  useEffect(() => {
    GetVersion().then(setVersion).catch(() => {
      /* Version is decoration; failing to read it must not blank the window. */
    })
  }, [])

  return (
    <div className="shell">
      <nav className="rail">
        <div className="rail__brand">
          <span className="rail__mark" />
          Hydra
        </div>
        <div className="rail__nav">
          {NAV.map((n) => (
            <button
              key={n.id}
              className="rail__item"
              aria-current={n.ready ? 'page' : undefined}
              disabled={!n.ready}
            >
              {n.label}
              {!n.ready && <span className="rail__soon">soon</span>}
            </button>
          ))}
        </div>
        <div className="rail__foot">
          {version ? `${version.version} · ${version.commit}` : ''}
        </div>
      </nav>

      <main className="main">
        {/* An error replaces the body but never the shell — a broken read
            should not look like a crashed app. */}
        {error && <div className="error">{error}</div>}
        {!error && data && <Dashboard data={data} />}
        {!error && !data && <p style={{ color: 'var(--hy-dim)' }}>Reading logs…</p>}
      </main>
    </div>
  )
}
