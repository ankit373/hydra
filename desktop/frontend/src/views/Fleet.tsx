import { useState } from 'react'
import type { Agent, Fleet as FleetData, Run } from '../types'
import { ms, pct, usdExact } from '../format'

export function Fleet({ data }: { data: FleetData }) {
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
            <RunCard key={r.id} run={r} groupThreshold={data.groupThreshold} />
          ))}
        </div>
      )}
    </>
  )
}

function RunCard({ run, groupThreshold }: { run: Run; groupThreshold: number }) {
  // Past the threshold a fan-out stops being readable as cards, so it collapses
  // to its state counts until asked to expand. A live run starts expanded —
  // it is the one you opened the app to watch.
  const large = run.allCount > groupThreshold
  const [expanded, setExpanded] = useState(!large)

  return (
    <section className={`run ${run.live ? 'run--live' : ''}`}>
      <header className="run__head">
        <span className={`run__dot ${run.live ? 'run__dot--live' : ''}`} />
        <span className="run__id">{run.id}</span>
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

          {expanded && run.agents.length > 0 && (
            <ul className="agents">
              {run.agents.map((a) => (
                <AgentRow key={a.id} agent={a} />
              ))}
            </ul>
          )}
        </>
      )}
    </section>
  )
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
