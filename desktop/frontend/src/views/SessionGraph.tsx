import { useMemo } from 'react'
import type { Agent, Session } from '../types'
import { layoutDag } from '../dagreLayout'

// Sugiyama/dagre layout mechanics live in dagreLayout.ts, shared with Fleet's
// inline run graph — see that file for why dagre over a force-directed layout.
const NODE_W = 168
const NODE_H = 46
// Wider than RunGraph's LABEL_MAX (12): this node has more room, but a full
// file path (an edit-target node's label) still needs truncating to fit it.
const LABEL_MAX = 24

interface Placed {
  id: string
  x: number
  y: number
  label: string
  sub: string
  state: string
}

export function SessionGraph({ session }: { session: Session }) {
  const { nodes, lines, width, height } = useMemo(() => layout(session), [session])

  if (nodes.length === 0) return null

  return (
    <div className="graph">
      <svg width={width} height={height} role="img" aria-label="Run graph">
        {lines.map((l, i) => (
          <polyline
            key={i}
            points={l.points}
            className={l.a2a ? 'edge edge--a2a' : 'edge'}
            fill="none"
          />
        ))}
        {nodes.map((n) => (
          <g key={n.id} transform={`translate(${n.x - NODE_W / 2},${n.y - NODE_H / 2})`}>
            <rect
              width={NODE_W}
              height={NODE_H}
              rx={10}
              className={`gnode gnode--${n.state}`}
            />
            <text x={11} y={19} className="gnode__label">
              {n.label}
            </text>
            <text x={11} y={34} className="gnode__sub">
              {n.sub}
            </text>
          </g>
        ))}
      </svg>
      <p className="graph__legend">
        solid = ownership · dashed = A2A handoff
      </p>
    </div>
  )
}

function shortLabel(s: string): string {
  if (s.length <= LABEL_MAX) return s
  // File paths: the filename at the end is the meaningful part, so truncate
  // from the start — clipping the end instead hides it (#461).
  if (s.includes('/')) return `…${s.slice(-(LABEL_MAX - 1))}`
  return `${s.slice(0, LABEL_MAX - 1)}…`
}

// A node with none of these signals never went through a run lifecycle — an
// edit-target node, say — so it isn't "pending" (still to run); it's an
// artifact the run touched. Reusing 'pending' reads as stuck forever (#462).
function stateClass(a: Agent): string {
  if (a.state && a.state !== 'pending') return a.state
  if (a.tier > 0 || a.durationMs > 0) return 'pending'
  return 'artifact'
}

function layout(session: Session) {
  const { nodes: placed, edges: lines, width, height } = layoutDag(
    session.agents.map((a) => ({ id: a.id, parent: a.parent })),
    session.edges.map((e) => ({ from: e.from, to: e.to, a2a: true })),
    { nodeW: NODE_W, nodeH: NODE_H },
  )

  const byID = new Map(session.agents.map((a) => [a.id, a]))
  const nodes: Placed[] = []
  for (const p of placed) {
    const a = byID.get(p.id)
    if (!a) continue
    nodes.push({
      id: p.id,
      x: p.x,
      y: p.y,
      label: shortLabel(a.model || a.head || a.id),
      // Verifiable facts first — tier, state, duration — rather than narration.
      sub: [a.tier > 0 ? `T${a.tier}` : null, a.state, a.durationMs > 0 ? `${a.durationMs}ms` : null]
        .filter(Boolean)
        .join(' · '),
      state: stateClass(a),
    })
  }

  return { nodes, lines, width, height }
}
