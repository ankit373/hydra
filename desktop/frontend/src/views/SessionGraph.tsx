import { useMemo } from 'react'
import type { Agent, Session } from '../types'
import { layoutDag } from '../dagreLayout'
import { graphLabel } from './graphLabel'

// Sugiyama/dagre layout mechanics live in dagreLayout.ts, shared with Fleet's
// inline run graph, see that file for why dagre over a force-directed layout.
// 180, not 168: the label starts at x=11 and the longest LABEL_MAX string
// measures 158px, so 168 left it 1px over its own border with no right
// padding at all while having 11px on the left. 11 + 158 + 11 = 180.
const NODE_W = 180
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
  /** The file this node represents, when state is 'artifact', clicking it
   *  opens that file rather than nothing (#518). */
  file?: string
}

export function SessionGraph({
  session,
  onOpenFile,
}: {
  session: Session
  onOpenFile?: (file: string) => void
}) {
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
          )
        })}
      </svg>
      <p className="graph__legend">
        solid = ownership · dashed = A2A handoff
      </p>
    </div>
  )
}

// A node with none of these signals never went through a run lifecycle, an
// edit-target node, say, so it isn't "pending" (still to run); it's an
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
    const state = stateClass(a)
    nodes.push({
      id: p.id,
      x: p.x,
      y: p.y,
      label: graphLabel(a.model || a.head || a.id, LABEL_MAX),
      // Verifiable facts first, tier, state, duration, rather than narration.
      sub: [a.tier > 0 ? `T${a.tier}` : null, a.state, a.durationMs > 0 ? `${a.durationMs}ms` : null]
        .filter(Boolean)
        .join(' · '),
      state,
      // An artifact node's own id IS the file path. graphLabel needs no
      // branch for that: it keeps the tail either way, and the `/` sniff that
      // used to pick the branch matched every model id too (#1120).
      file: state === 'artifact' ? a.id : undefined,
    })
  }

  return { nodes, lines, width, height }
}
