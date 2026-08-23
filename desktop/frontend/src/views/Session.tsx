import { useState } from 'react'
import type { Edit, Session as SessionData, TimelineEntry } from '../types'
import { clockTime, ms, pct, usdExact } from '../format'
import { SessionGraph } from './SessionGraph'
import { Code } from './Code'
import { TierTrack } from './TierTrack'

export function Session({
  session,
  edits,
  onBack,
  initialTab,
  initialFile,
}: {
  session: SessionData
  edits: Edit[]
  onBack: () => void
  /** Set when Session is opened by clicking an artifact node elsewhere (e.g.
   *  Fleet's inline graph, #518) — App keys Session by runId, so this only
   *  needs to seed initial state, not stay in sync afterward. */
  initialTab?: 'code'
  initialFile?: string
}) {
  // Timeline is the default: most runs are linear, and a list is the right
  // shape for a linear thing.
  const [tab, setTab] = useState<'timeline' | 'code' | 'graph'>(initialTab ?? 'timeline')
  // Consumed by Code, which re-selects whenever this changes — set once here,
  // then updated by clicking further artifact nodes within the same session.
  const [codeFile, setCodeFile] = useState<string | undefined>(initialFile)

  return (
    <>
      <header className="view__head">
        <div className="view__headrow">
          <button className="back" onClick={onBack}>
            ← Fleet
          </button>
          <h1 className="view__title">
            <span className="session__id">{session.runId}</span>
            {session.live && <span className="session__live">live</span>}
          </h1>
        </div>
      </header>

      {session.error && <div className="error">unreadable: {session.error}</div>}

      {!session.error && !session.found && (
        <div className="empty">
          <p className="empty__title">No log for this run</p>
          <p>It may have been cleaned up, or nothing was ever written for it.</p>
        </div>
      )}

      {session.found && (
        <>
          {session.skipped > 0 && (
            <p className="run__warn">
              {session.skipped} event{session.skipped === 1 ? '' : 's'} could not be attributed to an
              agent
            </p>
          )}

          <div className="tabs">
            <button
              className="tab"
              aria-current={tab === 'timeline' ? 'page' : undefined}
              onClick={() => setTab('timeline')}
            >
              Timeline
            </button>
            <button
              className="tab"
              aria-current={tab === 'code' ? 'page' : undefined}
              onClick={() => setTab('code')}
            >
              Code
            </button>
            {/* Graph appears only when a list genuinely cannot show the shape.
                Drawing a graph of a straight line is worse than a list. */}
            {session.nonLinear && (
              <button
                className="tab"
                aria-current={tab === 'graph' ? 'page' : undefined}
                onClick={() => setTab('graph')}
              >
                Graph
              </button>
            )}
          </div>

          {tab === 'code' ? (
            <Code
              runID={session.runId}
              edits={edits}
              initialFile={codeFile}
            />
          ) : tab === 'graph' && session.nonLinear ? (
            <SessionGraph
              session={session}
              onOpenFile={(file) => {
                setCodeFile(file)
                setTab('code')
              }}
            />
          ) : (
            <>
              <TierTrack entries={session.timeline} />
              <Timeline entries={session.timeline} />
            </>
          )}
        </>
      )}
    </>
  )
}

export function Timeline({ entries }: { entries: TimelineEntry[] }) {
  if (entries.length === 0) return null
  return (
    <ol className="timeline">
      {entries.map((e, i) => (
        <li key={i} className={`tl tl--${e.status || kindClass(e.kind)}`}>
          <span className="tl__time">{e.ts ? clockTime(e.ts) : '—'}</span>
          <span className="tl__kind">{e.kind.replace(/_/g, ' ')}</span>
          <span className="tl__who">
            {e.model || e.head || e.nodeId || ''}
            {e.tier > 0 && <span className="agent__tier">T{e.tier}</span>}
          </span>
          {/* Evidence leads. For an SPRT sample this is
              "agreed · LLR +1.200 → Λ 1.200" — what actually happened to the
              log-odds, ahead of any narration. */}
          {e.detail && <span className="tl__detail">{e.detail}</span>}
          {/* Joined, not concatenated with trailing separators: an entry with a
              confidence but no duration or cost used to render "86.0% · ". */}
          <span className="tl__meta">{meta(e)}</span>
        </li>
      ))}
    </ol>
  )
}

/** The right-hand facts, with separators only between present values. */
function meta(e: TimelineEntry): string {
  const parts: string[] = []
  if (e.confidence > 0) parts.push(pct(e.confidence * 100, 1))
  if (e.durationMs > 0) parts.push(ms(e.durationMs))
  if (e.costUsd > 0) parts.push(usdExact(e.costUsd))
  return parts.join(' · ')
}

function kindClass(kind: string): string {
  if (kind === 'error') return 'failed'
  if (kind === 'run_started' || kind === 'task_started') return 'running'
  return 'pending'
}
