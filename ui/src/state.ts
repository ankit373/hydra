import { execSync } from 'child_process'
import { readFileSync, existsSync } from 'fs'
import { homedir } from 'os'
import type { SystemState, ClaudeMode, PoolStatus } from './types.js'

import { fileURLToPath } from 'url'
import { dirname, join } from 'path'

const __filename = fileURLToPath(import.meta.url)
const __dirname = dirname(__filename)

// HYDRA_HOME = where dispatch/ and registry/ live (scripts, read-only)
// Priority: env var → auto-detect repo layout → ~/.hydra (installed)
function resolveHydraHome(): string {
  if (process.env.HYDRA_HOME) return process.env.HYDRA_HOME
  const repoGuess = join(__dirname, '../..')
  if (existsSync(join(repoGuess, 'dispatch/route.sh'))) return repoGuess
  return join(homedir(), '.hydra')
}

// HYDRA_DATA = where logs/ and state live (mutable, per-user)
function resolveHydraData(): string {
  if (process.env.HYDRA_DATA) return process.env.HYDRA_DATA
  return join(homedir(), '.hydra')
}

export const HYDRA      = resolveHydraHome()
const        HYDRA_DATA = resolveHydraData()
export const STATE_FILE = join(HYDRA_DATA, 'logs/state.json')
export const AUTH_FILE  = join(HYDRA_DATA, 'logs/auth_required.json')

function claudeMode(pct: number): ClaudeMode {
  if (pct >= 80) return 'emergency'
  if (pct >= 75) return 'critical'
  if (pct >= 70) return 'warning'
  if (pct >= 65) return 'caution'
  if (pct >= 50) return 'compact'
  return 'normal'
}

export function loadState(): SystemState {
  let claudePct = 0
  let exhaustedPools: string[] = []
  let authRequired = false
  let authUrl: string | null = null
  let authPool: string | null = null

  if (existsSync(STATE_FILE)) {
    try {
      const raw = JSON.parse(readFileSync(STATE_FILE, 'utf8'))
      claudePct = raw.claude_pct ?? 0
      exhaustedPools = raw.exhausted_pools ?? []
    } catch {}
  }

  if (existsSync(AUTH_FILE)) {
    try {
      const raw = JSON.parse(readFileSync(AUTH_FILE, 'utf8'))
      authRequired = true
      authUrl = raw.auth_url ?? null
      authPool = raw.pool ?? null
    } catch {}
  }

  const poolDefs = [
    { id: 'agy_claude',      label: 'Claude (agy)' },
    { id: 'agy_gemini_pro',  label: 'Gemini Pro'   },
    { id: 'agy_flash',       label: 'Flash'        },
    { id: 'agy_gpt',         label: 'GPT-OSS'      },
    { id: 'local_ollama',    label: 'Qwen (local)' },
  ]

  const pools = poolDefs.map(p => {
    if (authRequired && authPool === p.id) return { ...p, status: 'auth_required' as PoolStatus }
    return { ...p, status: (exhaustedPools.includes(p.id) ? 'exhausted' : 'ok') as PoolStatus }
  })

  return {
    claudePct,
    mode: claudeMode(claudePct),
    pools,
    lastTier: null,
    lastModel: null,
    lastEnum: null,
    lastStatus: null,
    authRequired,
    authUrl,
    authPool,
  }
}

export async function dispatch(enumKey: string, prompt: string): Promise<string> {
  return new Promise((resolve, reject) => {
    const { spawn } = require('child_process') as typeof import('child_process')
    const child = spawn(
      `${HYDRA}/dispatch/route.sh`,
      ['--enum', enumKey, '--prompt', prompt],
      { env: { ...process.env }, timeout: 300_000 }
    )
    let out = '', err = ''
    child.stdout?.on('data', (d: Buffer) => { out += d.toString() })
    child.stderr?.on('data', (d: Buffer) => { err += d.toString() })
    child.on('close', (code: number | null, signal: NodeJS.Signals | null) => {
      if (code === 0) resolve(out.trim())
      else if (signal) reject(new Error(err.trim() || `killed by signal ${signal}`))
      else reject(new Error(err.trim() || `exit ${code}`))
    })
  })
}
