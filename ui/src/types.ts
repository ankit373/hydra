export type PoolStatus = 'ok' | 'exhausted' | 'auth_required' | 'unknown'
export type ClaudeMode = 'normal' | 'compact' | 'caution' | 'warning' | 'critical' | 'emergency'

export interface Pool {
  id: string
  label: string
  status: PoolStatus
}

export interface SystemState {
  claudePct: number
  mode: ClaudeMode
  pools: Pool[]
  lastTier: number | null
  lastModel: string | null
  lastEnum: string | null
  lastStatus: 'ok' | 'fail' | 'escalated' | null
  authRequired: boolean
  authUrl: string | null
  authPool: string | null
}

export interface Message {
  id: string
  role: 'user' | 'hydra' | 'system' | 'error'
  content: string
  tier?: number
  model?: string
  enumKey?: string
  timestamp: Date
}

export const ENUM_KEYS = [
  'GRUNT', 'TRIVIAL', 'SIMPLE', 'STANDARD',
  'MODERATE', 'COMPLEX', 'HARD', 'VERY_HARD', 'EXPERT', 'CORE'
] as const
export type EnumKey = typeof ENUM_KEYS[number]

export const TIER_LABELS: Record<number, { model: string; pool: string; color: string }> = {
  1:  { model: 'Claude Core',          pool: 'claude_direct',   color: 'magenta' },
  2:  { model: 'Opus Thinking',         pool: 'agy_claude',     color: 'red'     },
  3:  { model: 'Sonnet Thinking',       pool: 'agy_claude',     color: 'red'     },
  4:  { model: 'GPT-OSS 120B',          pool: 'agy_gpt',        color: 'yellow'  },
  5:  { model: 'Pro High',              pool: 'agy_gemini_pro', color: 'blue'    },
  6:  { model: 'Pro Low',               pool: 'agy_gemini_pro', color: 'blue'    },
  7:  { model: 'Flash High',            pool: 'agy_flash',      color: 'green'   },
  8:  { model: 'Flash Med',             pool: 'agy_flash',      color: 'green'   },
  9:  { model: 'Flash Low',             pool: 'agy_flash',      color: 'green'   },
  10: { model: 'Qwen (local)',          pool: 'local_ollama',   color: 'cyan'    },
}

export const ENUM_TO_TIER: Record<EnumKey, number> = {
  CORE: 1, EXPERT: 2, VERY_HARD: 3, HARD: 4,
  COMPLEX: 5, MODERATE: 6, STANDARD: 7, SIMPLE: 8, TRIVIAL: 9, GRUNT: 10,
}
