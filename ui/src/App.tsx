import React, { useState, useEffect, useCallback } from 'react'
import { Box, Text, useInput, useApp } from 'ink'
import StatusPanel from './components/StatusPanel.js'
import ChatView from './components/ChatView.js'
import InputBar from './components/InputBar.js'
import { loadState, dispatch, STATE_FILE, AUTH_FILE } from './state.js'
import type { Message, EnumKey, SystemState } from './types.js'
import { ENUM_KEYS, ENUM_TO_TIER, TIER_LABELS } from './types.js'

function uid() { return Math.random().toString(36).slice(2) }

// Guess the right enum key from task text
function classify(text: string): EnumKey {
  const t = text.toLowerCase()
  if (/\b(scaffold|boilerplate|empty|stub|template|create folder)\b/.test(t)) return 'GRUNT'
  if (/\b(enum|constant|config file|barrel)\b/.test(t)) return 'TRIVIAL'
  if (/\b(dto|schema|interface|type def|model class)\b/.test(t)) return 'SIMPLE'
  if (/\b(controller|handler|endpoint|route|crud)\b/.test(t)) return 'STANDARD'
  if (/\b(service|repository|use case|repo)\b/.test(t)) return 'MODERATE'
  if (/\b(middleware|auth|guard|interceptor|pipeline)\b/.test(t)) return 'COMPLEX'
  if (/\b(review|tradeoff|rubber duck|second opinion)\b/.test(t)) return 'HARD'
  if (/\b(algorithm|refactor|complex|multi.file)\b/.test(t)) return 'VERY_HARD'
  if (/\b(architect|design|adr|rfc|strategy|system design)\b/.test(t)) return 'EXPERT'
  if (/\b(core business|critical path|security core)\b/.test(t)) return 'CORE'
  return 'STANDARD' // safe default
}

export default function App() {
  const { exit } = useApp()
  const [messages, setMessages] = useState<Message[]>([])
  const [input, setInput] = useState('')
  const [dispatching, setDispatching] = useState(false)
  const [state, setState] = useState<SystemState>(loadState())
  const [selectedEnum, setSelectedEnum] = useState<EnumKey>('STANDARD')
  const [dispatchInfo, setDispatchInfo] = useState<{ enum: string; tier: number; model: string } | undefined>()

  // Refresh system state every 5s — preserve dispatch history fields
  useEffect(() => {
    const t = setInterval(() => {
      const fresh = loadState()
      setState(prev => ({ ...fresh, lastTier: prev.lastTier, lastModel: prev.lastModel, lastEnum: prev.lastEnum, lastStatus: prev.lastStatus }))
    }, 5000)
    return () => clearInterval(t)
  }, [])

  // Tab cycles the enum key
  useInput((_, key) => {
    if (key.tab && !dispatching) {
      setSelectedEnum(prev => {
        const i = ENUM_KEYS.indexOf(prev)
        return ENUM_KEYS[(i + 1) % ENUM_KEYS.length] ?? 'STANDARD'
      })
    }
    if (key.escape) exit()
  })

  const addMsg = useCallback((msg: Omit<Message, 'id' | 'timestamp'>) => {
    setMessages(prev => [...prev, { ...msg, id: uid(), timestamp: new Date() }])
  }, [])

  const handleCommand = useCallback((cmd: string) => {
    const parts = cmd.slice(1).trim().split(/\s+/)
    switch (parts[0]) {
      case 'help':
        addMsg({ role: 'system', content: `Commands: /status /tiers /set-pct <n> /reset-pools /auth /auth-clear /clear /quit\nTab = cycle enum key` })
        break
      case 'status':
        const s = loadState()
        setState(prev => ({ ...s, lastTier: prev.lastTier, lastModel: prev.lastModel, lastEnum: prev.lastEnum, lastStatus: prev.lastStatus }))
        addMsg({ role: 'system', content: `Claude: ${s.claudePct}% [${s.mode}] | Pools: ${s.pools.map(p => `${p.label}:${p.status}`).join(', ')}${s.authRequired ? ` | 🔐 AUTH: ${s.authUrl}` : ''}` })
        break
      case 'auth':
        const authState = loadState()
        if (authState.authRequired && authState.authUrl) {
          addMsg({ role: 'system', content: `🔐 Auth required for pool: ${authState.authPool}\n\nURL: ${authState.authUrl}\n\nCopy this URL to your browser. After login, run /auth-clear` })
          try {
            const { execSync: exec } = require('child_process') as typeof import('child_process')
            exec(`open "${authState.authUrl}" 2>/dev/null || xdg-open "${authState.authUrl}" 2>/dev/null || true`)
          } catch {}
        } else {
          addMsg({ role: 'system', content: '✓ No auth required — all pools healthy' })
        }
        break
      case 'auth-clear':
        try {
          const { execSync: execClear } = require('child_process') as typeof import('child_process')
          execClear(`rm -f "${AUTH_FILE}"`)
          setState(loadState())
          addMsg({ role: 'system', content: '✓ Auth flag cleared' })
        } catch { addMsg({ role: 'error', content: 'Failed to clear auth flag' }) }
        break
      case 'tiers':
        const tierList = Object.entries(TIER_LABELS).map(([t, v]) => `T${t} ${v.model}`).join(' | ')
        addMsg({ role: 'system', content: tierList })
        break
      case 'set-pct':
        const pct = parseInt(parts[1] ?? '0', 10)
        if (!isNaN(pct)) {
          const { execSync } = require('child_process') as typeof import('child_process')
          const stateFile = STATE_FILE
          try {
            execSync(`jq '.claude_pct = ${pct}' ${stateFile} > ${stateFile}.tmp && mv ${stateFile}.tmp ${stateFile}`)
            setState(loadState())
            addMsg({ role: 'system', content: `Claude context set to ${pct}%` })
          } catch { addMsg({ role: 'error', content: 'Failed to update state' }) }
        }
        break
      case 'reset-pools':
        const { execSync: ex2 } = require('child_process') as typeof import('child_process')
        const sf = STATE_FILE
        try {
          ex2(`jq '.exhausted_pools = []' ${sf} > ${sf}.tmp && mv ${sf}.tmp ${sf}`)
          setState(loadState())
          addMsg({ role: 'system', content: 'All pools reset to ok' })
        } catch { addMsg({ role: 'error', content: 'Failed to reset pools' }) }
        break
      case 'clear':
        setMessages([])
        break
      case 'quit': case 'exit':
        exit()
        break
      default:
        addMsg({ role: 'error', content: `Unknown command: ${cmd}` })
    }
  }, [addMsg, exit])

  const handleSubmit = useCallback(async (text: string) => {
    if (!text.trim() || dispatching) return
    setInput('')

    if (text.startsWith('/')) {
      handleCommand(text)
      return
    }

    addMsg({ role: 'user', content: text })

    const enumKey = classify(text) !== 'STANDARD' ? classify(text) : selectedEnum
    const tier = ENUM_TO_TIER[enumKey]
    const model = TIER_LABELS[tier]?.model ?? 'unknown'

    setDispatching(true)
    setDispatchInfo({ enum: enumKey, tier, model })
    addMsg({ role: 'system', content: `classifying → ${enumKey} → T${tier}: ${model}` })

    try {
      const output = await dispatch(enumKey, text)
      addMsg({ role: 'hydra', content: output, tier, model, enumKey })
      setState(prev => ({ ...prev, lastTier: tier, lastModel: model, lastEnum: enumKey, lastStatus: 'ok' }))
    } catch (e: any) {
      addMsg({ role: 'error', content: e.message ?? 'dispatch failed' })
      setState(prev => ({ ...prev, lastTier: tier, lastModel: model, lastEnum: enumKey, lastStatus: 'fail' }))
    } finally {
      setDispatching(false)
      setDispatchInfo(undefined)
    }
  }, [dispatching, selectedEnum, addMsg, handleCommand])

  const modeColor: Record<string, string> = {
    normal: 'green', compact: 'cyan', caution: 'yellow',
    warning: 'yellow', critical: 'red', emergency: 'redBright',
  }

  return (
    <Box flexDirection="column" height={process.stdout.rows ?? 40}>
      {/* Header */}
      <Box borderStyle="single" borderColor="cyan" paddingX={1} justifyContent="space-between">
        <Text bold color="cyan">🐍  HYDRA</Text>
        <Text dimColor>multi-model orchestrator</Text>
        <Box>
          <Text color={modeColor[state.mode] ?? 'white'}>{state.mode.toUpperCase()}</Text>
          <Text dimColor>  │  Claude: </Text>
          <Text color={state.claudePct >= 70 ? 'red' : 'green'}>{state.claudePct}%</Text>
          <Text dimColor>  │  Esc: quit  Tab: cycle tier</Text>
        </Box>
      </Box>

      {/* Body */}
      <Box flexGrow={1} flexDirection="row">
        <StatusPanel state={state} />
        <ChatView
          messages={messages}
          dispatching={dispatching}
          dispatchInfo={dispatchInfo}
        />
      </Box>

      {/* Input */}
      <InputBar
        value={input}
        onChange={setInput}
        onSubmit={handleSubmit}
        selectedEnum={selectedEnum}
        onCycleEnum={() => setSelectedEnum(prev => {
          const i = ENUM_KEYS.indexOf(prev)
          return ENUM_KEYS[(i + 1) % ENUM_KEYS.length] ?? 'STANDARD'
        })}
        disabled={dispatching}
      />
    </Box>
  )
}
