import { useState } from 'react'
import type { Agent, Fleet as FleetData, Run } from '../types'
import { ms, pct, usdExact } from '../format'
import { RunGraph } from './RunGraph'

export function Fleet({
  data,
  onOpen,
  onOpenFile,
  onStartTask,
}: {
  data: FleetData
  onOpen: (runID: string) => void
  onOpenFile: (runID: string, file: string) => void
  onStartTask: () => void
}) {
  return (
    <>
      <header className="view__head">
        <h1 className="view__title">Activity</h1>
        <p className="view__sub">{activitySummary(data)}</p>
      </header>

      {!data.hasRuns ? (
        <div className="empty">
          <p className="empty__title">Nothing has run yet</p>
          <p>Ask for something in Chat, or run it from a terminal instead:</p>
          <button className="empty__cta" onClick={onStartTask}>
            Start a task
          </button>
          <p className="empty__alt">
            <code>hyctl dispatch --enum SIMPLE "…"</code>
          </p>
        </div>
      ) : (
        <div className="runs">
          {groupRuns(data.runs).map((g) => (
            <div className="rgroup" key={g.id}>
              {/* Headed only when there is more than one group to tell apart.
                  A lone "Done" heading over every run a machine has ever made
                  labels nothing. */}
              {g.headed && (
                <div className={`rgroup__head rgroup__head--${g.id}`}>
                  <span className="rgroup__lbl">{g.label}</span>
                  <span className="rgroup__n">{g.runs.length}</span>
                  {g.note && <span className="rgroup__note">{g.note}</span>}
                </div>
              )}
              {g.runs.map((r) => (
                <RunCard
                  key={r.id}
                  run={r}
                  groupThreshold={data.groupThreshold}
                  onOpen={onOpen}
                  onOpenFile={onOpenFile}
                />
              ))}
            </div>
          ))}
        </div>
      )}
    </>
  )
}

function RunCard({
  run,
  groupThreshold,
  onOpen,
  onOpenFile,
}: {
  run: Run
  groupThreshold: number
  onOpen: (runID: string) => void
  onOpenFile: (runID: string, file: string) => void
}) {
  // Past the threshold a node-link graph is a hairball, not a picture (mirrors
  // Airflow's separate Grid view for large DAGs), so it collapses to a state
  // heatmap until asked to expand. A live run starts expanded, it is the one
  // you opened the app to watch.
  const large = run.allCount > groupThreshold
  const [expanded, setExpanded] = useState(!large)

  return (
    <section className={`run ${run.live ? 'run--live' : ''}`}>
      <header className="run__head">
        <span className={`run__dot ${run.live ? 'run__dot--live' : ''}`} />
        <button className="run__open" onClick={() => onOpen(run.id)}>
          {/* A run with no recorded prompt falls back to its id, which is all
              it has. Saying "untitled" would hide the one handle that works. */}
          <span className={run.goal ? 'run__goal' : 'run__goal run__goal--id'}>
            {run.goal || run.id}
          </span>
          {run.goal && <span className="run__id">{run.id}</span>}
        </button>
        <span className="run__meta">
          {ms(run.elapsedMs)} · {usdExact(run.costUsd)}
          {run.confidence > 0 && ` · ${pct(run.confidence * 100, 1)} confidence`}
        </span>
      </header>

      {run.error ? (
        // A run whose log cannot be read still gets a row. Saying why beats
        // dropping it and rendering a partial fleet as a complete one.
        <p className="run__error">unreadable: {run.error}</p>
      ) : (
        <>
          <StateBar run={run} />

          {run.skipped > 0 && (
            <p className="run__warn">
              {run.skipped} event{run.skipped === 1 ? '' : 's'} could not be attributed to an agent
            </p>
          )}

          {large && (
            <button className="run__toggle" onClick={() => setExpanded((v) => !v)}>
              {expanded ? 'Collapse' : `Show all ${run.allCount} agents`}
            </button>
          )}

          {/* Below the threshold: a single agent needs no graph, and 2..N draw
              the same dagre DAG SessionGraph uses, just smaller. At/above the
              threshold the toggle above reveals a heatmap grid instead. */}
          {!large && (
            <RunShape agents={run.agents} onOpenFile={(file) => onOpenFile(run.id, file)} />
          )}
          {large && expanded && run.agents.length > 0 && <AgentGrid agents={run.agents} />}
        </>
      )}
    </section>
  )
}

/** The 0/1/2..N shape for a run below GroupThreshold. */
function RunShape({
  agents,
  onOpenFile,
}: {
  agents: Agent[]
  onOpenFile: (file: string) => void
}) {
  if (agents.length === 0) return null
  // A single node is trivially linear, the same reasoning Session.tsx uses
  // to decide nonLinear (isNonLinear is false whenever there's nothing to
  // branch from). Drawing a graph of one box is worse than a line of text.
  if (agents.length === 1) {
    return (
      <ul className="agents">
        <AgentRow agent={agents[0]} />
      </ul>
    )
  }
  return <RunGraph agents={agents} onOpenFile={onOpenFile} />
}

function StateBar({ run }: { run: Run }) {
  const parts: Array<[string, number]> = [
    ['running', run.running],
    ['ok', run.ok],
    ['failed', run.failed],
    ['pending', run.pending],
  ]
  const shown = parts.filter(([, n]) => n > 0)
  if (shown.length === 0) return null
  return (
    <div className="states">
      {shown.map(([state, n]) => (
        <span key={state} className={`state state--${state}`}>
          {n} {state}
        </span>
      ))}
    </div>
  )
}

function AgentRow({ agent }: { agent: Agent }) {
  return (
    <li className="agent" style={{ paddingLeft: `${agent.depth * 18}px` }}>
      <span className={`state-dot state-dot--${agent.state || 'pending'}`} />
      <span className="agent__id">{agent.model || agent.head || agent.id}</span>
      {agent.tier > 0 && <span className="agent__tier">T{agent.tier}</span>}
      <span className="agent__meta">
        {agent.durationMs > 0 && ms(agent.durationMs)}
        {agent.costUsd > 0 && ` · ${usdExact(agent.costUsd)}`}
        {agent.confidence > 0 && ` · ${pct(agent.confidence * 100, 1)}`}
      </span>
      {agent.detail && <span className="agent__detail">{agent.detail}</span>}
    </li>
  )
}

/**
 * At/above GroupThreshold a node-link graph is a hairball, not a picture,
 * Airflow ships a separate Grid view for exactly this reason instead of
 * scaling its Graph view. One chip per agent, colored by state, grouped by
 * depth: "mostly green with a cluster of red" has to read in under a second.
 */
function AgentGrid({ agents }: { agents: Agent[] }) {
  const byDepth = new Map<number, Agent[]>()
  for (const a of agents) {
    const row = byDepth.get(a.depth)
    if (row) row.push(a)
    else byDepth.set(a.depth, [a])
  }
  const depths = [...byDepth.keys()].sort((x, y) => x - y)

  return (
    <div className="agrid">
      {depths.map((d) => (
        <div className="agrid__row" key={d}>
          <span className="agrid__depth">D{d}</span>
          <div className="agrid__chips">
            {byDepth.get(d)!.map((a) => (
              <span
                key={a.id}
                className={`chip chip--${a.state || 'pending'}`}
                title={chipTitle(a)}
              />
            ))}
          </div>
        </div>
      ))}
    </div>
  )
}

function chipTitle(a: Agent): string {
  return [
    a.model || a.head || a.id,
    a.tier > 0 ? `T${a.tier}` : null,
    a.state,
    a.durationMs > 0 ? ms(a.durationMs) : null,
    a.costUsd > 0 ? usdExact(a.costUsd) : null,
  ]
    .filter(Boolean)
    .join(' · ')
}

/**
 * Activity grouped by what you would do about it, rather than by time alone.
 *
 * A flat newest-first list gives a parked run, a live one and a month-old
 * success the same weight, so nothing reads as needing attention. The order is
 * by who has to act: you, then the machine, then nobody.
 *
 * There is deliberately no "has a deliverable" group yet. Nothing in a run
 * records an artifact, a PR url, a file, a report, so a group for it would
 * be permanently empty or, worse, guessed at.
 */
type RunGroup = {
  id: 'waiting' | 'running' | 'attention' | 'done'
  label: string
  note?: string
  runs: Run[]
  headed: boolean
}

export function groupRuns(runs: Run[]): RunGroup[] {
  const waiting: Run[] = []
  const running: Run[] = []
  const attention: Run[] = []
  const done: Run[] = []

  for (const r of runs) {
    // Order matters: a parked run is also not live, and a live run may already
    // have a failed agent while still working. First match wins.
    if (r.waiting) waiting.push(r)
    else if (r.live) running.push(r)
    else if (r.failed > 0) attention.push(r)
    else done.push(r)
  }

  const all: RunGroup[] = [
    { id: 'waiting', label: 'Waiting on you', note: 'stopped until you answer', runs: waiting, headed: true },
    { id: 'running', label: 'Running now', runs: running, headed: true },
    { id: 'attention', label: 'Something failed', runs: attention, headed: true },
    { id: 'done', label: 'Done', runs: done, headed: true },
  ]
  const groups = all.filter((g) => g.runs.length > 0)

  // One group is not a grouping.
  if (groups.length < 2) return groups.map((g) => ({ ...g, headed: false }))
  return groups
}

/** Leads with whatever needs a person, and says plainly when nothing does. */
export function activitySummary(data: FleetData): string {
  const w = data.waitingCount
  const l = data.liveCount
  if (w > 0 && l > 0) {
    return `${w} waiting on you · ${l} still running`
  }
  if (w > 0) {
    return `${w} ${w === 1 ? 'request needs' : 'requests need'} an answer from you`
  }
  if (l > 0) {
    return `${l} request${l === 1 ? '' : 's'} running now`
  }
  return 'Nothing needs you. Every request this machine has handled, newest first.'
}
