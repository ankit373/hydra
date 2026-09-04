import { useEffect, useState } from 'react'
import { GetMCPServers, SyncMCPRegistry } from '../bindings'
import type { MCPPanel, MCPServer } from '../types'

/**
 * The MCP servers an agent on this machine can call, and what is known about
 * whether each was ever safe to run with your credentials.
 *
 * internal/mcpregistry has shipped since #588 with no desktop surface at all,
 * so the app could tell you an agent touched a tool and nothing about the
 * server behind it. Fetches its own data rather than taking it on the Audit
 * view's prop, which keeps that view presentational and fixture-testable.
 */
export function MCPServers() {
  const [panel, setPanel] = useState<MCPPanel | null>(null)
  const [syncing, setSyncing] = useState(false)
  const [syncNote, setSyncNote] = useState('')

  const load = () => void GetMCPServers().then(setPanel).catch(() => {})
  useEffect(load, [])

  async function sync() {
    if (syncing) return
    setSyncing(true)
    setSyncNote('')
    try {
      const r = await SyncMCPRegistry()
      setSyncNote(r.error ? r.error : `Pulled ${r.servers} server records.`)
      if (!r.error) load()
    } catch (e) {
      setSyncNote(e instanceof Error ? e.message : String(e))
    } finally {
      setSyncing(false)
    }
  }

  if (!panel) return null

  return (
    <section>
      <div className="mcp__head">
        <h2 className="section__title">MCP servers</h2>
        <button className="mcp__sync" onClick={() => void sync()} disabled={syncing}>
          {syncing ? 'Checking the registry…' : 'Check the registry'}
        </button>
      </div>

      {/* Without a sync every server reads unresolved, and that says nothing
          about the servers — only that nothing has been compared yet. Saying
          so is the difference between a finding and an absence of one. */}
      <p className="card__note">
        {panel.synced
          ? `Compared against the official registry, last pulled ${panel.synced.slice(0, 10)}.`
          : 'The official registry has never been pulled on this machine, so every server below is unresolved for that reason alone — not because anything is wrong with it.'}
      </p>
      {syncNote && <p className="card__note">{syncNote}</p>}

      {panel.error && <p className="card__note">Could not scan: {panel.error}</p>}

      {!panel.error && panel.servers.length === 0 && (
        <p className="card__note">
          {panel.scanned
            ? 'No MCP servers are configured in any client this machine knows about.'
            : 'The scan did not run, so this is not a statement that none are installed.'}
        </p>
      )}

      {panel.servers.length > 0 && (
        <div className="tablewrap">
          <table className="tbl">
            <thead>
              <tr>
                <th>Server</th>
                <th>Where it is configured</th>
                <th>Launched by</th>
                <th>Trust</th>
                <th>Score</th>
              </tr>
            </thead>
            <tbody>
              {panel.servers.map((s) => (
                <tr key={`${s.client}/${s.scope}/${s.name}`}>
                  <td>
                    <span className="mcp__name">{s.name}</span>
                    {s.package && <span className="mcp__pkg">{s.package}</span>}
                    {s.nearestMatch && (
                      <span className="mcp__near">
                        looks like {s.nearestMatch}
                        {typeof s.nearestDist === 'number' && s.nearestDist > 0
                          ? ` (${s.nearestDist} character${s.nearestDist === 1 ? '' : 's'} apart)`
                          : ''}
                      </span>
                    )}
                  </td>
                  <td>
                    {s.client} · {s.scope}
                  </td>
                  <td>{s.remote ? 'remote url' : s.command || '—'}</td>
                  <td>
                    <span className={`mcp__state mcp__state--${s.state || s.status}`}>
                      {stateText(s)}
                    </span>
                  </td>
                  <td>{scoreText(s)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  )
}

/** Plain words for the lifecycle state, and honest when there is no verdict. */
export function stateText(s: MCPServer): string {
  switch (s.state) {
    case 'trusted':
      return 'trusted'
    case 'provisional':
      return 'awaiting re-check'
    case 'flagged':
      return 'flagged'
    case 'quarantined':
      return 'quarantined'
    case 'delisted':
      return 'delisted'
    case 'new':
      return 'newly seen'
    default:
      return s.status === 'verified' ? 'in the registry' : 'not in the registry'
  }
}

/**
 * A score is only a number when its confidence says it is one. Every other
 * MCP directory prints a figure it cannot support, which is the thing the
 * registry was built not to do.
 */
export function scoreText(s: MCPServer): string {
  if (!s.scored) return 'not scored'
  if (s.confidence === 'insufficient_evidence') return 'insufficient evidence'
  return `${Math.round(s.score)}/100 · ${(s.confidence || '').replace(/_/g, ' ')}`
}
