import { useCallback, useEffect, useState } from 'react'
import { GetDashboard, GetFleet, GetVersion } from './bindings'
import type { Dashboard as DashboardData, Fleet as FleetData, Version } from './types'
import { Dashboard } from './views/Dashboard'
import { Fleet } from './views/Fleet'

/** Dashboard is retrospective — a slow refresh is enough and costs nothing. */
const DASHBOARD_MS = 5000

/**
 * Fleet polls faster because it answers "what is happening now", and
 * runlog.StaleAfter is 10s — a slower tick than half that would let a run look
 * live for seconds after it died.
 */
const FLEET_MS = 2000

// Session and Code arrive in Phases 5-6. They are listed disabled rather than
// hidden so the shell's shape is honest about where this is going, and rendered
// as "soon" rather than as empty views that look broken.
const NAV = [
  { id: 'dashboard', label: 'Dashboard', ready: true },
  { id: 'fleet', label: 'Fleet', ready: true },
  { id: 'session', label: 'Session', ready: false },
  { id: 'code', label: 'Code', ready: false },
] as const

type ViewID = (typeof NAV)[number]['id']

export default function App() {
  const [view, setView] = useState<ViewID>('dashboard')
  const [dashboard, setDashboard] = useState<DashboardData | null>(null)
  const [fleet, setFleet] = useState<FleetData | null>(null)
  const [version, setVersion] = useState<Version | null>(null)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async (which: ViewID) => {
    try {
      if (which === 'fleet') setFleet(await GetFleet())
      else setDashboard(await GetDashboard())
      setError(null)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [])

  // Only the visible view polls: a background tick on a view nobody is looking
  // at is pure cost.
  useEffect(() => {
    void load(view)
    const every = view === 'fleet' ? FLEET_MS : DASHBOARD_MS
    const t = setInterval(() => void load(view), every)
    return () => clearInterval(t)
  }, [load, view])

  useEffect(() => {
    GetVersion().then(setVersion).catch(() => {
      /* Version is decoration; failing to read it must not blank the window. */
    })
  }, [])

  const data = view === 'fleet' ? fleet : dashboard

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
              aria-current={view === n.id ? 'page' : undefined}
              disabled={!n.ready}
              onClick={() => n.ready && setView(n.id)}
            >
              {n.label}
              {!n.ready && <span className="rail__soon">soon</span>}
              {n.id === 'fleet' && (fleet?.liveCount ?? 0) > 0 && (
                <span className="rail__live">{fleet?.liveCount}</span>
              )}
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
        {!error && view === 'dashboard' && dashboard && <Dashboard data={dashboard} />}
        {!error && view === 'fleet' && fleet && <Fleet data={fleet} />}
        {!error && !data && <p style={{ color: 'var(--hy-dim)' }}>Reading logs…</p>}
      </main>
    </div>
  )
}
