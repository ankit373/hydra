import dagre from '@dagrejs/dagre'

/**
 * Layered (Sugiyama) layout via dagre.
 *
 * Deterministic and near-linear per relayout, and it has a notion of
 * direction, which matches the causal semantics of a run. A force-directed
 * or stress-majorization model has neither: it treats edges as springs, so
 * the same run lays out differently frame to frame and "downstream" means
 * nothing.
 *
 * The layout maths is bought, not hand-rolled. Crossing minimisation is
 * NP-hard and dagre already implements the standard heuristic chain
 * (Brandes-Köpf coordinate assignment included).
 *
 * Shared by SessionGraph (a run's detail view) and Fleet's inline run graph
 * so both surfaces agree on what a run's shape looks like, only node size
 * and label content differ per caller.
 */

export interface LayoutNodeInput {
  id: string
  /** Ownership edge: parent -> id. Absent or unknown parents are ignored. */
  parent?: string
}

export interface LayoutEdgeInput {
  from: string
  to: string
  /** True for a collaboration/handoff edge, distinct from ownership. */
  a2a?: boolean
}

export interface PlacedNode {
  id: string
  x: number
  y: number
}

export interface PlacedEdge {
  points: string
  a2a: boolean
}

export interface LayoutResult {
  nodes: PlacedNode[]
  edges: PlacedEdge[]
  width: number
  height: number
}

export interface LayoutSize {
  nodeW: number
  nodeH: number
  nodesep?: number
  ranksep?: number
  /** Floor for the returned width/height, so a tiny graph still gets a frame. */
  minWidth?: number
  minHeight?: number
}

export function layoutDag(
  nodes: LayoutNodeInput[],
  edges: LayoutEdgeInput[],
  size: LayoutSize,
): LayoutResult {
  const g = new dagre.graphlib.Graph()
  // Top-to-bottom: a run reads as time flowing downward.
  g.setGraph({
    rankdir: 'TB',
    nodesep: size.nodesep ?? 26,
    ranksep: size.ranksep ?? 42,
    marginx: 16,
    marginy: 16,
  })
  g.setDefaultEdgeLabel(() => ({}))

  for (const n of nodes) {
    g.setNode(n.id, { width: size.nodeW, height: size.nodeH })
  }
  for (const n of nodes) {
    if (n.parent && g.hasNode(n.parent)) g.setEdge(n.parent, n.id, { a2a: false })
  }
  // Non-ownership edges (e.g. A2A handoffs) are laid out too, so a target is
  // placed sensibly rather than floating, but they stay visually distinct.
  for (const e of edges) {
    if (g.hasNode(e.from) && g.hasNode(e.to)) g.setEdge(e.from, e.to, { a2a: Boolean(e.a2a) })
  }

  dagre.layout(g)

  const placed: PlacedNode[] = []
  for (const id of g.nodes()) {
    const p = g.node(id)
    if (!p) continue
    placed.push({ id, x: p.x, y: p.y })
  }

  const lines: PlacedEdge[] = []
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
    nodes: placed,
    edges: lines,
    width: Math.max(graph.width ?? 0, size.minWidth ?? 320),
    height: Math.max(graph.height ?? 0, size.minHeight ?? 120),
  }
}
