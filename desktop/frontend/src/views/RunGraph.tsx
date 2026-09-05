import { useMemo } from 'react'
import type { Agent } from '../types'
import { layoutDag } from '../dagreLayout'

// Deliberately smaller than SessionGraph's NODE_W/H (168x46): Fleet shows one
// of these per run, several runs per screen, so a node here carries only a
// short label, no tier/duration subtext. Same .gnode--* state classes as
// SessionGraph though, so a run's shape reads identically in both views.
const NODE_W = 92
const NODE_H = 26
const LABEL_MAX = 12

interface Placed {
  id: string
  x: number
  y: number
  label: string
  state: string
  /** The file this node represents, when state is 'artifact' (#518). */
  file?: string
}

/** Compact inline DAG for a run's agents, Fleet's 2..GroupThreshold tier. */
export function RunGraph({
  agents,
  onOpenFile,
}: {
  agents: Agent[]
  onOpenFile?: (file: string) => void
}) {
  const { nodes, lines, width, height } = useMemo(() => layout(agents), [agents])

  if (nodes.length === 0) return null

  return (
    <div className="graph graph--compact">
      <svg width={width} height={height} role="img" aria-label="Run graph">
        {lines.map((l, i) => (
          <polyline key={i} points={l.points} className="edge" fill="none" />
        ))}
        {nodes.map((n) => {
          const clickable = n.file !== undefined && onOpenFile !== undefined
          return (
            <g
              key={n.id}
              transform={`translate(${n.x - NODE_W / 2},${n.y - NODE_H / 2})`}
              className={clickable ? 'gnode-wrap--clickable' : undefined}
              role={clickable ? 'button' : undefined}
              tabIndex={clickable ? 0 : undefined}
              aria-label={clickable ? `Open ${n.file}` : undefined}
              onClick={clickable ? () => onOpenFile!(n.file!) : undefined}
              onKeyDown={
                clickable
                  ? (e) => {
                      if (e.key === 'Enter' || e.key === ' ') {
                        e.preventDefault()
                        onOpenFile!(n.file!)
                      }
                    }
                  : undefined
              }
            >
              <rect
                width={NODE_W}
                height={NODE_H}
                rx={7}
                className={`gnode gnode--${n.state}`}
              />
              <text x={8} y={17} className="gnode__label">
                {n.label}
              </text>
            </g>
          )
        })}
      </svg>
    </div>
  )
}

function shortLabel(a: Agent): string {
  const s = a.model || a.head || a.id
  return s.length > LABEL_MAX ? `${s.slice(0, LABEL_MAX - 1)}…` : s
}

// A node with none of these signals never went through a run lifecycle, an
// edit-target node, say, so it isn't "pending" (still to run); it's an
// artifact the run touched. Reusing 'pending' reads as stuck forever (#462).
function stateClass(a: Agent): string {
  if (a.state && a.state !== 'pending') return a.state
  if (a.tier > 0 || a.durationMs > 0) return 'pending'
  return 'artifact'
}

function layout(agents: Agent[]) {
  const { nodes: placed, edges: lines, width, height } = layoutDag(
    agents.map((a) => ({ id: a.id, parent: a.parent })),
    [],
    { nodeW: NODE_W, nodeH: NODE_H, nodesep: 14, ranksep: 26, minWidth: 0, minHeight: 0 },
  )

  const byID = new Map(agents.map((a) => [a.id, a]))
  const nodes: Placed[] = []
  for (const p of placed) {
    const a = byID.get(p.id)
    if (!a) continue
    const state = stateClass(a)
    nodes.push({
      id: p.id,
      x: p.x,
      y: p.y,
      label: shortLabel(a),
      state,
      file: state === 'artifact' ? a.id : undefined,
    })
  }

  return { nodes, lines, width, height }
}
