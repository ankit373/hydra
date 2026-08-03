import { useMemo } from 'react'
import dagre from '@dagrejs/dagre'
import type { Session } from '../types'

/**
 * Layered (Sugiyama) layout via dagre.
 *
 * Deterministic and near-linear per relayout, and it has a notion of direction —
 * which matches the causal semantics of a run. A force-directed or
 * stress-majorization model has neither: it treats edges as springs, so the same
 * run lays out differently frame to frame and "downstream" means nothing.
 *
 * The layout maths is bought, not hand-rolled. Crossing minimisation is NP-hard
 * and dagre already implements the standard heuristic chain (Brandes-Köpf
 * coordinate assignment included).
 */
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

interface Line {
  points: string
  a2a: boolean
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
  const g = new dagre.graphlib.Graph()
  // Top-to-bottom: a run reads as time flowing downward, and that matches the
  // timeline it sits next to.
  g.setGraph({ rankdir: 'TB', nodesep: 26, ranksep: 42, marginx: 16, marginy: 16 })
  g.setDefaultEdgeLabel(() => ({}))

  for (const a of session.agents) {
    g.setNode(a.id, { width: NODE_W, height: NODE_H })
  }
  for (const a of session.agents) {
    if (a.parent && g.hasNode(a.parent)) g.setEdge(a.parent, a.id, { a2a: false })
  }
  // A2A edges are laid out too, so a handoff target is placed sensibly rather
  // than floating — but they stay visually distinct from ownership.
  for (const e of session.edges) {
    if (g.hasNode(e.from) && g.hasNode(e.to)) g.setEdge(e.from, e.to, { a2a: true })
  }

  dagre.layout(g)

  const byID = new Map(session.agents.map((a) => [a.id, a]))
  const nodes: Placed[] = []
  for (const id of g.nodes()) {
    const p = g.node(id)
    const a = byID.get(id)
    if (!p || !a) continue
    nodes.push({
      id,
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

  const lines: Line[] = []
  for (const e of g.edges()) {
    const pts = g.edge(e)?.points ?? []
    if (pts.length === 0) continue
    lines.push({
      points: pts.map((pt: { x: number; y: number }) => `${pt.x},${pt.y}`).join(' '),
      a2a: Boolean(g.edge(e)?.a2a),
    })
  }

  const graph = g.graph()
  return {
    nodes,
    lines,
    width: Math.max(graph.width ?? 0, 320),
    height: Math.max(graph.height ?? 0, 120),
  }
}
