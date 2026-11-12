// The theme follows the OS unless the viewer picks one. The pick is kept in this
// webview's storage, which the inline script in index.html applies before CSS paints.
import { SaveTheme } from './bindings'

export type ThemeMode = 'system' | 'light' | 'dark'

export const THEME_KEY = 'hydra-theme'
export const THEME_LABEL: Record<ThemeMode, string> = {
  system: 'Theme: match system',
  light: 'Theme: light',
  dark: 'Theme: dark',
}

/** The order goes to the theme the OS is not showing first, so the first press
 *  always changes the screen: system, the other theme, the OS's own, system. */
export function nextTheme(mode: ThemeMode): ThemeMode {
  const osDark = window.matchMedia?.('(prefers-color-scheme: dark)').matches ?? false
  const order: ThemeMode[] = osDark ? ['system', 'light', 'dark'] : ['system', 'dark', 'light']
  return order[(order.indexOf(mode) + 1) % order.length]
}

export function currentTheme(): ThemeMode {
  const t = document.documentElement.dataset.theme
  return t === 'light' || t === 'dark' ? t : 'system'
}

export function setTheme(mode: ThemeMode): void {
  const root = document.documentElement
  if (mode === 'system') delete root.dataset.theme
  else root.dataset.theme = mode
  try {
    if (mode === 'system') localStorage.removeItem(THEME_KEY)
    else localStorage.setItem(THEME_KEY, mode)
  } catch {
    // Storage refused: this session still follows the choice.
  }
  // The window's launch colour; failing to record it costs one flash, not the choice.
  SaveTheme(mode).catch(() => {})
  paintWindow()
}

/** The native window shows before the first frame and behind overscroll, so it
 *  takes the page's background whenever the theme in force changes. */
export function paintWindow(): void {
  const set = window.runtime?.WindowSetBackgroundColour
  if (!set) return
  const hex = getComputedStyle(document.documentElement).getPropertyValue('--hy-color-surface-base').trim()
  const [r, g, b] = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16))
  if ([r, g, b].some(Number.isNaN)) return
  set(r, g, b, 255)
}

export function watchSystemTheme(): void {
  window.matchMedia?.('(prefers-color-scheme: dark)').addEventListener?.('change', paintWindow)
  paintWindow()
}
