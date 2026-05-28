import React from 'react'
import { Box, Text } from 'ink'
import TextInput from 'ink-text-input'
import type { EnumKey } from '../types.js'
import { ENUM_KEYS } from '../types.js'

export default function InputBar({
  value,
  onChange,
  onSubmit,
  selectedEnum,
  onCycleEnum,
  disabled,
}: {
  value: string
  onChange: (v: string) => void
  onSubmit: (v: string) => void
  selectedEnum: EnumKey
  onCycleEnum: () => void
  disabled: boolean
}) {
  return (
    <Box
      borderStyle="single"
      borderColor="cyan"
      paddingX={1}
      flexDirection="row"
    >
      <Text color="cyan" bold>▸ </Text>
      <Box flexGrow={1}>
        <TextInput
          value={value}
          onChange={onChange}
          onSubmit={onSubmit}
          placeholder={disabled ? 'dispatching…' : 'type a task or /command'}
        />
      </Box>
      <Box marginLeft={1}>
        <Text dimColor>[</Text>
        <Text
          color="green"
          bold
          // Tab cycles enum key
          onClick={onCycleEnum}
        >
          {selectedEnum}
        </Text>
        <Text dimColor>]</Text>
      </Box>
    </Box>
  )
}
