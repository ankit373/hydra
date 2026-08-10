import { useMemo } from 'react'
import type { Session } from '../types'
import { layoutDag } from '../dagreLayout'

// Sugiyama/dagre layout mechanics live in dagreLayout.ts, shared with Fleet's
// inline run graph — see that file for why dagre over a force-directed layout.
const NODE_W = 168
const NODE_H = 46

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
              className={`gnode gnode--${n.state || 'pending'}`}
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
      label: a.model || a.head || a.id,
      // Verifiable facts first — tier, state, duration — rather than narration.
      sub: [a.tier > 0 ? `T${a.tier}` : null, a.state, a.durationMs > 0 ? `${a.durationMs}ms` : null]
        .filter(Boolean)
        .join(' · '),
      state: a.state,
    })
  }

  return { nodes, lines, width, height }
}
