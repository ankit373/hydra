import { useState } from 'react'
import type { Edit, Session as SessionData, TimelineEntry } from '../types'
import { clockTime, ms, pct, usdExact } from '../format'
import { SessionGraph } from './SessionGraph'
import { Code } from './Code'
import { TierTrack } from './TierTrack'
import { PageHeader } from './PageHeader'
import { Tabs } from './Tabs'

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
   *  Fleet's inline graph, #518), App keys Session by runId, so this only
   *  needs to seed initial state, not stay in sync afterward. */
  initialTab?: 'code'
  initialFile?: string
}) {
  // Timeline is the default: most runs are linear, and a list is the right
  // shape for a linear thing.
  const [tab, setTab] = useState<'timeline' | 'code' | 'graph'>(initialTab ?? 'timeline')
  // Consumed by Code, which re-selects whenever this changes, set once here,
  // then updated by clicking further artifact nodes within the same session.
  const [codeFile, setCodeFile] = useState<string | undefined>(initialFile)

  return (
    <>
      <PageHeader
        title={session.goal || session.runId}
        // The run id is provenance, not a subtitle: it is how you find this run
        // again in the logs, and it sits in the same slot every view uses for
        // "what this rests on".
        provenance={session.goal ? session.runId : undefined}
        actions={
          <>
            {session.live && <span className="session__live">live</span>}
            <button className="pagehead__btn" onClick={onBack}>
              ← Activity
            </button>
          </>
        }
      />

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

          <Tabs
            label="What this run did"
            panelID="session-panel"
            current={tab}
            onSelect={setTab}
            tabs={[
              { id: 'timeline', label: 'Timeline', count: session.timeline.length },
              { id: 'code', label: 'Code', count: edits.length },
              // Graph appears only when a list genuinely cannot show the shape.
              // Drawing a graph of a straight line is worse than a list.
              ...(session.nonLinear ? [{ id: 'graph' as const, label: 'Graph' }] : []),
            ]}
          />

          <div id="session-panel" role="tabpanel">
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
          </div>
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
              "agreed · LLR +1.200 → Λ 1.200", what actually happened to the
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
