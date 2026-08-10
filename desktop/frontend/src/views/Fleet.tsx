import { useState } from 'react'
import type { Agent, Fleet as FleetData, Run } from '../types'
import { ms, pct, usdExact } from '../format'
import { RunGraph } from './RunGraph'

export function Fleet({ data, onOpen }: { data: FleetData; onOpen: (runID: string) => void }) {
  return (
    <>
      <header className="view__head">
        <h1 className="view__title">Fleet</h1>
        <p className="view__sub">
          {data.liveCount > 0
            ? `${data.liveCount} run${data.liveCount === 1 ? '' : 's'} in flight`
            : 'Nothing running right now.'}
        </p>
      </header>

      {!data.hasRuns ? (
        <div className="empty">
          <p className="empty__title">No runs yet</p>
          <p>
            Run <code>hyctl dispatch --enum SIMPLE "…"</code> and it appears here while it works.
          </p>
        </div>
      ) : (
        <div className="runs">
          {data.runs.map((r) => (
            <RunCard key={r.id} run={r} groupThreshold={data.groupThreshold} onOpen={onOpen} />
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
}: {
  run: Run
  groupThreshold: number
  onOpen: (runID: string) => void
}) {
  // Past the threshold a node-link graph is a hairball, not a picture (mirrors
  // Airflow's separate Grid view for large DAGs), so it collapses to a state
  // heatmap until asked to expand. A live run starts expanded — it is the one
  // you opened the app to watch.
  const large = run.allCount > groupThreshold
  const [expanded, setExpanded] = useState(!large)

  return (
    <section className={`run ${run.live ? 'run--live' : ''}`}>
      <header className="run__head">
        <span className={`run__dot ${run.live ? 'run__dot--live' : ''}`} />
        <button className="run__id run__id--link" onClick={() => onOpen(run.id)}>
          {run.id}
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
          {!large && <RunShape agents={run.agents} />}
          {large && expanded && run.agents.length > 0 && <AgentGrid agents={run.agents} />}
        </>
      )}
    </section>
  )
}

/** The 0/1/2..N shape for a run below GroupThreshold. */
function RunShape({ agents }: { agents: Agent[] }) {
  if (agents.length === 0) return null
  // A single node is trivially linear — the same reasoning Session.tsx uses
  // to decide nonLinear (isNonLinear is false whenever there's nothing to
  // branch from). Drawing a graph of one box is worse than a line of text.
  if (agents.length === 1) {
    return (
      <ul className="agents">
        <AgentRow agent={agents[0]} />
      </ul>
    )
  }
  return <RunGraph agents={agents} />
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
 * At/above GroupThreshold a node-link graph is a hairball, not a picture —
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
