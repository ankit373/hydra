import React from 'react'
import { Box, Text } from 'ink'
import type { SystemState } from '../types.js'
import { TIER_LABELS } from '../types.js'

const MODE_COLOR: Record<string, string> = {
  normal:    'green',
  compact:   'cyan',
  caution:   'yellow',
  warning:   'yellow',
  critical:  'red',
  emergency: 'redBright',
}

function TokenBar({ pct }: { pct: number }) {
  const filled = Math.round(pct / 10)
  const bar = '█'.repeat(filled) + '░'.repeat(10 - filled)
  const color = pct >= 75 ? 'red' : pct >= 50 ? 'yellow' : 'green'
  return (
    <Box>
      <Text color={color}>{bar}</Text>
      <Text> {pct}%</Text>
    </Box>
  )
}

export default function StatusPanel({ state }: { state: SystemState }) {
  const modeColor = MODE_COLOR[state.mode] ?? 'white'

  return (
    <Box
      flexDirection="column"
      width={22}
      borderStyle="single"
      borderColor="gray"
      paddingX={1}
    >
      <Text bold color="cyan">SYSTEM</Text>
      <Text> </Text>

      <Text dimColor>Claude context</Text>
      <TokenBar pct={state.claudePct} />
      <Text color={modeColor} bold>[{state.mode.toUpperCase()}]</Text>

      <Text> </Text>
      <Text bold dimColor>TOKEN POOLS</Text>
      {state.pools.map(p => {
        const icon = p.status === 'ok' ? '✓' : p.status === 'auth_required' ? '🔐' : '✗'
        const color = p.status === 'ok' ? 'green' : p.status === 'auth_required' ? 'yellow' : 'red'
        return (
          <Box key={p.id}>
            <Text color={color}>{icon}</Text>
            <Text> {p.label}</Text>
          </Box>
        )
      })}

      {state.authRequired && (
        <>
          <Text> </Text>
          <Text color="yellow" bold>⚠ AUTH NEEDED</Text>
          <Text dimColor>/auth to get link</Text>
        </>
      )}

      {state.lastTier != null && (
        <>
          <Text> </Text>
          <Text bold dimColor>LAST DISPATCH</Text>
          <Text>
            T{state.lastTier}{' '}
            <Text color={TIER_LABELS[state.lastTier]?.color ?? 'white'}>
              {state.lastEnum}
            </Text>
          </Text>
          <Text dimColor>{state.lastModel}</Text>
          <Text color={state.lastStatus === 'ok' ? 'green' : 'red'}>
            {state.lastStatus === 'ok' ? '✓ done' : '✗ failed'}
          </Text>
        </>
      )}

      <Text> </Text>
      <Text bold dimColor>TIERS</Text>
      {([1,2,3,4,5,6,7,8,9,10] as const).map(t => (
        <Text key={`tier-${t}`} dimColor={t > 3}>
          <Text color={TIER_LABELS[t]?.color}>T{t}</Text>
          {' '}{TIER_LABELS[t]?.model.slice(0, 11)}
        </Text>
      ))}
    </Box>
  )
}
