import { useCallback, useEffect, useMemo, useState } from "react";
import {
  CheckHyctl,
  GetDashboard,
  GetEdits,
  GetFleet,
  GetHeads,
  GetPendingQuestions,
  GetSecurity,
  GetSession,
  GetVersion,
} from "./bindings";
import type {
  Dashboard as DashboardData,
  Edit,
  Fleet as FleetData,
  HeadPanel,
  HyctlStatus,
  SecurityReport,
  Session as SessionData,
  Version,
} from "./types";
import { Dashboard } from "./views/Dashboard";
import { Fleet } from "./views/Fleet";
import { Session } from "./views/Session";
import { Security as SecurityView } from "./views/Security";
import { Models } from "./views/Models";
import { Settings } from "./views/Settings";
import { ErrorBoundary } from "./ErrorBoundary";
import { ChatView } from "./views/ChatView";
import { SetupBanner } from "./views/SetupBanner";
import { AppHeader } from "./views/AppHeader";
import { AppFooter } from "./views/AppFooter";
import { ErrorState } from "./views/ErrorState";
import { CommandPalette, type Command } from "./views/CommandPalette";
import { HydraSpinner } from "./brand";

/** Dashboard is retrospective, a slow refresh is enough and costs nothing. */
const DASHBOARD_MS = 5000;

/**
 * Fleet and an open Session poll faster because they answer "what is happening
 * now", and runlog.StaleAfter is 10s, a slower tick than half that would let a
 * run look live for seconds after it died.
 */
const LIVE_MS = 2000;

/** The footer's spend, when Usage is not the open view and not already polling it. */
const CHROME_MS = 15000;

/** Heads come from probe.Run, which scans the machine, so the chrome asks rarely. */
const HEADS_MS = 60000;

/**
 * Industry vocabulary, from the signed-off design: Chat, Models, Activity,
 * Usage, Audit. The old labels were Hydra's internal names (dispatch, fleet,
 * governor), which is why the released build read as jargon.
 *
 * Labels, not glyphs. The rail shipped five unlabelled characters
 * (✎ ⌘ ≡ ▫ ⛨) whose meaning was carried entirely by a tooltip (#1060).
 */
// Six items measure 435px of header at the default padding, and the header
// stops fitting an 820px viewport at seven. 980 is main.go's MinWidth, but
// zooming narrows the CSS viewport below it, so app.css gives the tabs back
// their side padding under 900. A seventh item needs that re-measured (#1122).
const NAV = [
  { id: "chat", label: "Chat" },
  { id: "models", label: "Models" },
  { id: "activity", label: "Activity" },
  { id: "usage", label: "Usage" },
  { id: "audit", label: "Audit" },
  { id: "settings", label: "Settings" },
] as const;

/** Session is reached by opening one from Activity, never from the nav. */
type ViewID = (typeof NAV)[number]["id"] | "session";

/** What each view reads, named as the thing on disk, so a failure says which
 *  read failed rather than only that one did. */
const READS: Record<ViewID, string> = {
  chat: "the run log",
  models: "the model registry",
  activity: "the run log",
  usage: "the spend log",
  session: "this run's log",
  audit: "the security report",
  settings: "the config",
};

const titleFor = (v: ViewID) => NAV.find((n) => n.id === v)?.label ?? "Run";
const msg = (e: unknown) => (e instanceof Error ? e.message : String(e));

export default function App() {
  const [view, setView] = useState<ViewID>("chat");
  const [runID, setRunID] = useState<string>("");
  const [dashboard, setDashboard] = useState<DashboardData | null>(null);
  const [fleet, setFleet] = useState<FleetData | null>(null);
  // Parked tasks are visible from every view, not only the thread that
  // started one: a question nobody is looking at is the case this exists for.
  const [waiting, setWaiting] = useState(0);
  const [session, setSession] = useState<SessionData | null>(null);
  const [edits, setEdits] = useState<Edit[] | null>(null);
  const [security, setSecurity] = useState<SecurityReport | null>(null);
  const [version, setVersion] = useState<Version | null>(null);
  const [heads, setHeads] = useState<HeadPanel | null>(null);
  const [error, setError] = useState<string | null>(null);
  // A failed read and a read still in flight are different states, and both
  // used to render as null. "probing…" was what a dead backend looked like.
  const [headsError, setHeadsError] = useState<string | null>(null);
  const [fleetError, setFleetError] = useState<string | null>(null);
  const [hyctlStatus, setHyctlStatus] = useState<HyctlStatus | null>(null);
  const [paletteOpen, setPaletteOpen] = useState(false);

  // Fleet's empty state sends people here to start a task (#422). A counter
  // rather than a boolean so asking twice still moves the caret back to the
  // input instead of no-oping on an unchanged value.
  const [chatFocusSignal, setChatFocusSignal] = useState(0);
  const startTask = useCallback(() => {
    setView("chat");
    setChatFocusSignal((n) => n + 1);
  }, []);

  const load = useCallback(async (which: ViewID, id: string) => {
    try {
      if (which === "activity") setFleet(await GetFleet());
      // Code is a tab inside Session now (#519), not its own view, fetch
      // both together so switching tabs never has to wait on a second load.
      else if (which === "session") {
        const [s, e] = await Promise.all([GetSession(id), GetEdits(id)]);
        setSession(s);
        setEdits(e);
      } else if (which === "audit") setSecurity(await GetSecurity());
      else if (which === "usage") setDashboard(await GetDashboard());
      setError(null);
    } catch (e) {
      setError(msg(e));
    }
  }, []);

  // Only the visible view polls: a background tick on a view nobody is looking
  // at is pure cost.
  useEffect(() => {
    // Chat and Models drive their own reads, so there is nothing to tick here
    // for them.
    if (view === "chat" || view === "models" || view === "settings") return;
    void load(view, runID);
    const every = view === "usage" || view === "audit" ? DASHBOARD_MS : LIVE_MS;
    const t = setInterval(() => void load(view, runID), every);
    return () => clearInterval(t);
  }, [load, view, runID]);

  // Polled regardless of the visible view, unlike the per-view reads above:
  // the badge's whole job is to be seen from somewhere else.
  useEffect(() => {
    const tick = () =>
      void GetPendingQuestions()
        .then((q) => setWaiting(q.questions.length))
        .catch(() => {
          /* A failed read must not clear a badge that was correct. */
        });
    tick();
    const t = setInterval(tick, DASHBOARD_MS);
    return () => clearInterval(t);
  }, []);

  // The footer's spend, and the run list Chat's sidebar and the palette read.
  // Each is skipped while the view that owns it is open and already polling it
  // faster, so neither file is read twice on one tick. Polling fleet here is
  // also what finally makes the Activity badge count live runs from anywhere:
  // it only ever updated while Activity was the open view.
  useEffect(() => {
    const tick = () => {
      if (view !== "usage") {
        void GetDashboard()
          .then(setDashboard)
          .catch(() => {
            /* Chrome, not content: a failed read keeps the last known figure. */
          });
      }
      if (view !== "activity") {
        void GetFleet()
          .then((f) => {
            setFleet(f);
            setFleetError(null);
          })
          // Same again: a failed read must not empty a list that was correct,
          // but Chat's sidebar has to stop saying it is still reading.
          .catch((e) => setFleetError(msg(e)));
      }
    };
    tick();
    const t = setInterval(tick, CHROME_MS);
    return () => clearInterval(t);
  }, [view]);

  // Heads drive the header's routable count and the footer. probe.Run scans
  // the machine, so this ticks an order of magnitude slower than the rest.
  useEffect(() => {
    const tick = () =>
      void GetHeads()
        .then((h) => {
          setHeads(h);
          setHeadsError(null);
        })
        // The last count is still kept rather than reported as zero heads;
        // what is recorded is that the probe is no longer answering.
        .catch((e) => setHeadsError(msg(e)));
    tick();
    const t = setInterval(tick, HEADS_MS);
    return () => clearInterval(t);
  }, []);

  useEffect(() => {
    GetVersion()
      .then(setVersion)
      .catch(() => {
        /* Version is decoration; failing to read it must not blank the window. */
      });
  }, []);

  // One-shot, not polled: this is a first-run check (#383), not a live value.
  // A machine with hyctl already on PATH, the common case, never shows
  // anything, since the banner below renders only when Found is false.
  useEffect(() => {
    CheckHyctl()
      .then(setHyctlStatus)
      .catch(() => {
        /* Same as GetVersion: decoration, not worth blanking the window over. */
      });
  }, []);

  // Set alongside runID when a caller wants Session to open straight on the
  // Code tab at a specific file (an artifact-node click, #518) rather than
  // its own default. Session is keyed by runId below, so a fresh mount reads
  // this once as initial state, plain opens must clear it, or a later
  // no-file open of a different run would wrongly inherit it.
  const [pendingFile, setPendingFile] = useState<string | undefined>();

  const openSession = useCallback((id: string) => {
    setRunID(id);
    // Don't show the previous run's data under a new id.
    setSession(null);
    setEdits(null);
    setPendingFile(undefined);
    setView("session");
  }, []);

  const openSessionFile = useCallback((id: string, file: string) => {
    setRunID(id);
    setSession(null);
    setEdits(null);
    setPendingFile(file);
    setView("session");
  }, []);

  const selectNav = useCallback((id: string) => setView(id as ViewID), []);

  // ⌘K anywhere. Bound on the window rather than a field so it works from a
  // view that has focus in a textarea, which is where Chat leaves it.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setPaletteOpen((o) => !o);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const commands = useMemo<Command[]>(() => {
    const out: Command[] = NAV.map((n) => ({
      id: `view:${n.id}`,
      label: n.label,
      group: "Go to",
      run: () => setView(n.id),
    }));
    for (const r of fleet?.runs ?? []) {
      out.push({
        id: `run:${r.id}`,
        label: r.goal || r.id,
        group: "Recent runs",
        hint: r.live ? "running" : r.id,
        run: () => openSession(r.id),
      });
    }
    for (const h of heads?.heads ?? []) {
      out.push({
        id: `head:${h.id}`,
        label: h.id,
        group: "Heads",
        hint: h.routable ? `tier ${h.tier}` : "unroutable",
        run: () => setView("models"),
      });
    }
    return out;
  }, [fleet, heads, openSession]);

  const nav = useMemo(
    () =>
      NAV.map((n) => ({
        ...n,
        badge:
          n.id === "chat" ? waiting : n.id === "activity" ? (fleet?.liveCount ?? 0) : 0,
        live: n.id === "activity",
      })),
    [waiting, fleet],
  );

  // Dashboard handles its own loading state (a skeleton, not this fallback
  // text) so its first-load window can look like the rest of the view
  // instead of a plain sentence.
  const loading =
    (view === "activity" && !fleet) ||
    (view === "session" && (!session || !edits)) ||
    (view === "audit" && !security);

  // Session is a drill-in from Activity, so the nav keeps Activity lit rather
  // than lighting nothing at all.
  const navCurrent = view === "session" ? "activity" : view;

  return (
    <div className="shell">
      <AppHeader
        nav={nav}
        current={navCurrent}
        onSelect={selectNav}
        onSearch={() => setPaletteOpen(true)}
        routable={heads ? heads.routable : null}
        headsError={headsError}
        todayUsd={dashboard ? dashboard.spend.todayUsd : null}
        onHeads={() => setView("models")}
        onSpend={() => setView("usage")}
      />

      <main className={view === "chat" ? "main main--chat" : "main"}>
        {/* Non-blocking: it sits above whichever view is open rather than
            replacing it, and renders nothing at all once hyctl is found. */}
        {hyctlStatus && !hyctlStatus.found && (
          // Padded by its own wrapper: .main--chat has no padding of its own,
          // so an unwrapped banner sat flush against the window edge with its
          // accent bar clipped.
          <div className="banners">
            <SetupBanner status={hyctlStatus} onChanged={setHyctlStatus} />
          </div>
        )}

        {/* An error replaces the body but never the shell, a broken read
            should not look like a crashed app. */}
        {error && (
          <ErrorState
            title={titleFor(view)}
            what={READS[view]}
            detail={error}
            onRetry={() => void load(view, runID)}
          />
        )}
        {!error && view === "chat" && (
          <ErrorBoundary label="Chat">
            <ChatView
              onOpenRun={openSession}
              focusSignal={chatFocusSignal}
              runs={fleet?.runs ?? null}
              runsError={fleetError}
            />
          </ErrorBoundary>
        )}
        {!error && view === "models" && (
          <ErrorBoundary label="Models">
            <Models heads={heads} />
          </ErrorBoundary>
        )}
        {!error && view === "settings" && (
          <ErrorBoundary label="Settings">
            <Settings />
          </ErrorBoundary>
        )}
        {!error && view === "usage" && (
          <ErrorBoundary label="Usage">
            <Dashboard data={dashboard} onOpenRun={openSession} />
          </ErrorBoundary>
        )}
        {!error && view === "activity" && fleet && (
          <ErrorBoundary label="Activity">
            <Fleet
              data={fleet}
              onOpen={openSession}
              onOpenFile={openSessionFile}
              onStartTask={startTask}
            />
          </ErrorBoundary>
        )}
        {!error && view === "session" && session && edits && (
          // Keyed by runId: a fresh mount per run resets tab/codeFile state
          // instead of carrying over a Graph/Code selection from whatever run
          // was open before, and lets initialTab/initialFile seed cleanly.
          <Session
            key={session.runId}
            session={session}
            edits={edits}
            onBack={() => setView("activity")}
            initialTab={pendingFile ? "code" : undefined}
            initialFile={pendingFile}
          />
        )}
        {!error && view === "audit" && security && (
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

      <AppFooter
        version={version}
        routable={heads ? heads.routable : null}
        total={heads ? heads.heads.length : null}
        local={heads ? heads.heads.filter((h) => h.localOnly).length : null}
        headsError={headsError}
        todayUsd={dashboard ? dashboard.spend.todayUsd : null}
        calls={dashboard ? dashboard.spend.todayCalls : null}
        mode={dashboard?.governor.known ? dashboard.governor.effectiveMode : ""}
      />

      <CommandPalette
        open={paletteOpen}
        onClose={() => setPaletteOpen(false)}
        commands={commands}
      />
    </div>
  );
}
