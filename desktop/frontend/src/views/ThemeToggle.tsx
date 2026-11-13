import { useState, type ReactNode } from 'react'
import { currentTheme, nextTheme, setTheme, THEME_LABEL, type ThemeMode } from '../theme'

const ICON: Record<ThemeMode, ReactNode> = {
  system: (
    <>
      <circle cx="12" cy="12" r="8" />
      <path d="M12 4a8 8 0 0 1 0 16z" className="apphead__themefill" />
    </>
  ),
  light: (
    <>
      <circle cx="12" cy="12" r="4" />
      <path d="M12 2v3M12 19v3M2 12h3M19 12h3M4.9 4.9l2.1 2.1M17 17l2.1 2.1M4.9 19.1L7 17M17 7l2.1-2.1" />
    </>
  ),
  dark: <path d="M20 14.5A8 8 0 1 1 9.5 4a6.5 6.5 0 0 0 10.5 10.5z" />,
}

/** Shows the theme in force; each press moves to the next one (nextTheme). */
export function ThemeToggle() {
  const [mode, setMode] = useState<ThemeMode>(currentTheme)
  const [said, setSaid] = useState('')
  return (
    <>
      <button
        className="apphead__theme"
        aria-label={THEME_LABEL[mode]}
        title={`${THEME_LABEL[mode]}. Click to change.`}
        onClick={() => {
          const next = nextTheme(mode)
          setTheme(next)
          setMode(next)
          setSaid(THEME_LABEL[next])
        }}
      >
        <svg viewBox="0 0 24 24" aria-hidden="true">
          {ICON[mode]}
        </svg>
      </button>
      <span className="visually-hidden" role="status">
        {said}
      </span>
    </>
  )
}
