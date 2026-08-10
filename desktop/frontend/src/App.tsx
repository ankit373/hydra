import { useCallback, useEffect, useState } from 'react'
import { CheckHyctl, GetDashboard, GetEdits, GetFleet, GetSession, GetVersion } from './bindings'
import type {
  Dashboard as DashboardData,
  Edit,
  Fleet as FleetData,
  HyctlStatus,
  Session as SessionData,
  Version,
} from './types'
import { Dashboard } from './views/Dashboard'
import { Fleet } from './views/Fleet'
import { Session } from './views/Session'
import { Code } from './views/Code'
import { ChatDock } from './views/ChatDock'
import { UpdateNotice } from './views/UpdateNotice'
import { SetupBanner } from './views/SetupBanner'

/** Dashboard is retrospective — a slow refresh is enough and costs nothing. */
const DASHBOARD_MS = 5000

/**
 * Fleet and an open Session poll faster because they answer "what is happening
 * now", and runlog.StaleAfter is 10s — a slower tick than half that would let a
 * run look live for seconds after it died.
 */
const LIVE_MS = 2000

const NAV = [
  { id: 'dashboard', label: 'Dashboard', ready: true },
  { id: 'fleet', label: 'Fleet', ready: true },
  { id: 'session', label: 'Session', ready: true },
  { id: 'code', label: 'Code', ready: true },
] as const

type ViewID = (typeof NAV)[number]['id']

export default function App() {
  const [view, setView] = useState<ViewID>('dashboard')
  const [runID, setRunID] = useState<string>('')
  const [dashboard, setDashboard] = useState<DashboardData | null>(null)
  const [fleet, setFleet] = useState<FleetData | null>(null)
  const [session, setSession] = useState<SessionData | null>(null)
  const [edits, setEdits] = useState<Edit[] | null>(null)
  const [version, setVersion] = useState<Version | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [hyctlStatus, setHyctlStatus] = useState<HyctlStatus | null>(null)

  const load = useCallback(async (which: ViewID, id: string) => {
    try {
      if (which === 'fleet') setFleet(await GetFleet())
      else if (which === 'session') setSession(await GetSession(id))
      else if (which === 'code') setEdits(await GetEdits(id))
      else setDashboard(await GetDashboard())
      setError(null)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [])

  // Only the visible view polls: a background tick on a view nobody is looking
  // at is pure cost.
  useEffect(() => {
    void load(view, runID)
    const every = view === 'dashboard' ? DASHBOARD_MS : LIVE_MS
    const t = setInterval(() => void load(view, runID), every)
    return () => clearInterval(t)
  }, [load, view, runID])

  useEffect(() => {
    GetVersion().then(setVersion).catch(() => {
      /* Version is decoration; failing to read it must not blank the window. */
    })
  }, [])

  // One-shot, not polled: this is a first-run check (#383), not a live value.
  // A machine with hyctl already on PATH — the common case — never shows
  // anything, since the banner below renders only when Found is false.
  useEffect(() => {
    CheckHyctl().then(setHyctlStatus).catch(() => {
      /* Same as GetVersion: decoration, not worth blanking the window over. */
    })
  }, [])

  const openSession = useCallback((id: string) => {
    setRunID(id)
    // Don't show the previous run's data under a new id.
    setSession(null)
    setEdits(null)
    setView('session')
  }, [])

  // Session is reachable by drilling in from Fleet; selecting it with no run
  // chosen opens the most recent one, which is what "Session" means with no
  // further qualification.
  const selectNav = useCallback(
    (id: ViewID) => {
      if ((id === 'session' || id === 'code') && !runID) {
        const first = fleet?.runs?.[0]?.id
        if (!first) return
        setRunID(first)
        setSession(null)
        setEdits(null)
        setView(id)
        return
      }
      setView(id)
    },
    [fleet, runID, openSession],
  )

  const loading =
    (view === 'dashboard' && !dashboard) ||
    (view === 'fleet' && !fleet) ||
    (view === 'session' && !session) ||
    (view === 'code' && !edits)

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
              disabled={
                !n.ready ||
                ((n.id === 'session' || n.id === 'code') && !runID && !fleet?.runs?.length)
              }
              onClick={() => n.ready && selectNav(n.id)}
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
          <UpdateNotice />
        </div>
      </nav>

      <main className="main">
        {/* Non-blocking: it sits above whichever view is open rather than
            replacing it, and renders nothing at all once hyctl is found. */}
        {hyctlStatus && !hyctlStatus.found && (
          <SetupBanner status={hyctlStatus} onChanged={setHyctlStatus} />
        )}

        {/* An error replaces the body but never the shell — a broken read
            should not look like a crashed app. */}
        {error && <div className="error">{error}</div>}
        {!error && view === 'dashboard' && dashboard && <Dashboard data={dashboard} />}
        {!error && view === 'fleet' && fleet && <Fleet data={fleet} onOpen={openSession} />}
        {!error && view === 'session' && session && (
          <Session session={session} onBack={() => setView('fleet')} />
        )}
        {!error && view === 'code' && edits && (
          <>
            <header className="view__head">
              <button className="back" onClick={() => setView('session')}>
                ← Session
              </button>
              <h1 className="view__title">Code</h1>
              <p className="view__sub">What this run changed on disk.</p>
            </header>
            <Code runID={runID} edits={edits} />
          </>
        )}
        {!error && loading && <p style={{ color: 'var(--hy-dim)' }}>Reading logs…</p>}
      </main>

      <ChatDock onOpenRun={openSession} />
    </div>
  )
}
