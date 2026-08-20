import { useCallback, useEffect, useState } from 'react'
import { CheckHyctl, GetDashboard, GetEdits, GetFleet, GetSecurity, GetSession, GetVersion } from './bindings'
import type {
  Dashboard as DashboardData,
  Edit,
  Fleet as FleetData,
  HyctlStatus,
  SecurityReport,
  Session as SessionData,
  Version,
} from './types'
import { Dashboard } from './views/Dashboard'
import { Fleet } from './views/Fleet'
import { Session } from './views/Session'
import { Security as SecurityView } from './views/Security'
import { ErrorBoundary } from './ErrorBoundary'
import { ChatView } from './views/ChatView'
import { UpdateNotice } from './views/UpdateNotice'
import { SetupBanner } from './views/SetupBanner'
import { HydraMark, HydraSpinner } from './brand'

/** Dashboard is retrospective — a slow refresh is enough and costs nothing. */
const DASHBOARD_MS = 5000

/**
 * Fleet and an open Session poll faster because they answer "what is happening
 * now", and runlog.StaleAfter is 10s — a slower tick than half that would let a
 * run look live for seconds after it died.
 */
const LIVE_MS = 2000

// Chat first, and default: the app's job is to get work dispatched, and every
// other view is retrospective (#520). Glyphs keep the rail narrow enough that
// chat gets the width; label still ships as the tooltip and accessible name.
const NAV = [
  { id: 'chat', label: 'Chat', glyph: '\u270E', ready: true },
  { id: 'dashboard', label: 'Dashboard', glyph: '\u25EB', ready: true },
  { id: 'fleet', label: 'Fleet', glyph: '\u2318', ready: true },
  { id: 'session', label: 'Session', glyph: '\u2261', ready: true },
  { id: 'security', label: 'Security', glyph: '\u26E8', ready: true },
] as const

type ViewID = (typeof NAV)[number]['id']

export default function App() {
  const [view, setView] = useState<ViewID>('chat')
  const [runID, setRunID] = useState<string>('')
  const [dashboard, setDashboard] = useState<DashboardData | null>(null)
  const [fleet, setFleet] = useState<FleetData | null>(null)
  const [session, setSession] = useState<SessionData | null>(null)
  const [edits, setEdits] = useState<Edit[] | null>(null)
  const [security, setSecurity] = useState<SecurityReport | null>(null)
  const [version, setVersion] = useState<Version | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [hyctlStatus, setHyctlStatus] = useState<HyctlStatus | null>(null)

  // Fleet's empty state sends people here to start a task (#422). A counter
  // rather than a boolean so asking twice still moves the caret back to the
  // input instead of no-oping on an unchanged value.
  const [chatFocusSignal, setChatFocusSignal] = useState(0)
  const startTask = useCallback(() => {
    setView('chat')
    setChatFocusSignal((n) => n + 1)
  }, [])

  const load = useCallback(async (which: ViewID, id: string) => {
    try {
      if (which === 'fleet') setFleet(await GetFleet())
      // Code is a tab inside Session now (#519), not its own view — fetch
      // both together so switching tabs never has to wait on a second load.
      else if (which === 'session') {
        const [s, e] = await Promise.all([GetSession(id), GetEdits(id)])
        setSession(s)
        setEdits(e)
      } else if (which === 'security') setSecurity(await GetSecurity())
      else setDashboard(await GetDashboard())
      setError(null)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [])

  // Only the visible view polls: a background tick on a view nobody is looking
  // at is pure cost.
  useEffect(() => {
    // Chat drives its own reads (it polls the run it just started), so there is
    // nothing here to tick for it.
    if (view === 'chat') return
    void load(view, runID)
    const every = view === 'dashboard' || view === 'security' ? DASHBOARD_MS : LIVE_MS
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

  // Set alongside runID when a caller wants Session to open straight on the
  // Code tab at a specific file (an artifact-node click, #518) rather than
  // its own default. Session is keyed by runId below, so a fresh mount reads
  // this once as initial state — plain opens must clear it, or a later
  // no-file open of a different run would wrongly inherit it.
  const [pendingFile, setPendingFile] = useState<string | undefined>()

  const openSession = useCallback((id: string) => {
    setRunID(id)
    // Don't show the previous run's data under a new id.
    setSession(null)
    setEdits(null)
    setPendingFile(undefined)
    setView('session')
  }, [])

  const openSessionFile = useCallback((id: string, file: string) => {
    setRunID(id)
    setSession(null)
    setEdits(null)
    setPendingFile(file)
    setView('session')
  }, [])

  // Session is reachable by drilling in from Fleet; selecting it with no run
  // chosen opens the most recent one, which is what "Session" means with no
  // further qualification.
  const selectNav = useCallback(
    (id: ViewID) => {
      if (id === 'session' && !runID) {
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
    [fleet, runID],
  )

  // Dashboard handles its own loading state (a skeleton, not this fallback
  // text) so its first-load window can look like the rest of the view
  // instead of a plain sentence.
  const loading =
    (view === 'fleet' && !fleet) ||
    (view === 'session' && (!session || !edits)) ||
    (view === 'security' && !security)

  return (
    <div className="shell">
      <nav className="rail rail--icons" aria-label="Views">
        <HydraMark className="rail__mark" />
        <div className="rail__nav">
          {NAV.map((n) => (
            <button
              key={n.id}
              className="rail__item"
              aria-current={view === n.id ? 'page' : undefined}
              aria-label={n.label}
              title={n.label}
              disabled={!n.ready || (n.id === 'session' && !runID && !fleet?.runs?.length)}
              onClick={() => n.ready && selectNav(n.id)}
            >
              <span aria-hidden="true">{n.glyph}</span>
              {n.id === 'fleet' && (fleet?.liveCount ?? 0) > 0 && (
                <span className="rail__live">{fleet?.liveCount}</span>
              )}
            </button>
          ))}
        </div>
        <div className="rail__foot">
          <UpdateNotice />
          <span className="rail__ver" title={version ? `${version.version} · ${version.commit}` : ''}>
            {version ? version.version : ''}
          </span>
        </div>
      </nav>

      <main className={view === 'chat' ? 'main main--chat' : 'main'}>
        {/* Non-blocking: it sits above whichever view is open rather than
            replacing it, and renders nothing at all once hyctl is found. */}
        {hyctlStatus && !hyctlStatus.found && (
          <SetupBanner status={hyctlStatus} onChanged={setHyctlStatus} />
        )}

        {/* An error replaces the body but never the shell — a broken read
            should not look like a crashed app. */}
        {error && <div className="error">{error}</div>}
        {!error && view === 'chat' && (
          <ErrorBoundary label="Chat">
            <ChatView onOpenRun={openSession} focusSignal={chatFocusSignal} />
          </ErrorBoundary>
        )}
        {!error && view === 'dashboard' && <Dashboard data={dashboard} />}
        {!error && view === 'fleet' && fleet && (
          <Fleet
            data={fleet}
            onOpen={openSession}
            onOpenFile={openSessionFile}
            onStartTask={startTask}
          />
        )}
        {!error && view === 'session' && session && edits && (
          // Keyed by runId: a fresh mount per run resets tab/codeFile state
          // instead of carrying over a Graph/Code selection from whatever run
          // was open before, and lets initialTab/initialFile seed cleanly.
          <Session
            key={session.runId}
            session={session}
            edits={edits}
            onBack={() => setView('fleet')}
            initialTab={pendingFile ? 'code' : undefined}
            initialFile={pendingFile}
          />
        )}
        {!error && view === 'security' && security && (
          <ErrorBoundary label="Security">
            <SecurityView data={security} />
          </ErrorBoundary>
        )}
        {!error && loading && (
          <div className="loading">
            <HydraSpinner className="loading__mark" />
            <p>Reading logs…</p>
          </div>
        )}
      </main>
    </div>
  )
}
