import React from 'react'
import { Box, Text } from 'ink'
import Spinner from 'ink-spinner'
import type { Message } from '../types.js'
import { TIER_LABELS } from '../types.js'

function MessageRow({ msg }: { msg: Message }) {
  if (msg.role === 'user') {
    return (
      <Box marginBottom={1}>
        <Text color="cyan" bold>▸ </Text>
        <Text>{msg.content}</Text>
      </Box>
    )
  }

  if (msg.role === 'system') {
    return (
      <Box marginBottom={1}>
        <Text dimColor>  {msg.content}</Text>
      </Box>
    )
  }

  if (msg.role === 'error') {
    return (
      <Box marginBottom={1}>
        <Text color="red">✗ {msg.content}</Text>
      </Box>
    )
  }

  // hydra response
  const tierInfo = msg.tier ? TIER_LABELS[msg.tier] : null
  return (
    <Box flexDirection="column" marginBottom={1}>
      {msg.tier && (
        <Box>
          <Text dimColor>  routed via </Text>
          <Text color={tierInfo?.color ?? 'white'} bold>
            T{msg.tier} {msg.enumKey} ({msg.model})
          </Text>
        </Box>
      )}
      <Box marginLeft={2} flexDirection="column">
        {msg.content.split('\n').map((line, i) => (
          <Text key={i}>{line}</Text>
        ))}
      </Box>
    </Box>
  )
}

export default function ChatView({
  messages,
  dispatching,
  dispatchInfo,
}: {
  messages: Message[]
  dispatching: boolean
  dispatchInfo?: { enum: string; tier: number; model: string }
}) {
  const visible = messages.slice(-20)

  return (
    <Box flexDirection="column" flexGrow={1} paddingX={1} overflow="hidden">
      {visible.length === 0 && (
        <Box flexDirection="column" marginTop={2}>
          <Text bold color="cyan">🐍 Hydra</Text>
          <Text dimColor>Multi-model orchestration. Type a task or /help</Text>
          <Text> </Text>
          <Text dimColor>Examples:</Text>
          <Text dimColor>  write a User DTO in TypeScript</Text>
          <Text dimColor>  write a GitHub Actions CI pipeline</Text>
          <Text dimColor>  design the auth architecture</Text>
          <Text dimColor>  /status  /tiers  /set-pct 52  /help</Text>
        </Box>
      )}

      {visible.map(msg => (
        <MessageRow key={msg.id} msg={msg} />
      ))}

      {dispatching && dispatchInfo && (
        <Box>
          <Text color="green">
            <Spinner type="dots" />
          </Text>
          <Text dimColor>
            {' '}routing T{dispatchInfo.tier} {dispatchInfo.enum} → {dispatchInfo.model}
          </Text>
        </Box>
      )}
    </Box>
  )
}
