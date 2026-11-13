import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { ThemeToggle } from './ThemeToggle'
import { THEME_KEY } from '../theme'
import indexHtml from '../../index.html?raw'
import mockHtml from '../../__mock.html?raw'

afterEach(() => {
  cleanup()
  delete document.documentElement.dataset.theme
  localStorage.clear()
  vi.restoreAllMocks()
})

describe('ThemeToggle', () => {
  // jsdom reports no dark preference, so the OS here is light and the first
  // press goes to dark: a press that changes nothing on screen reads as broken.
  it('cycles system, the other theme, the OS theme and back, saving the choice', () => {
    render(<ThemeToggle />)
    const button = () => screen.getByRole('button')
    expect(button()).toHaveAccessibleName('Theme: match system')
    fireEvent.click(button())
    expect(document.documentElement.dataset.theme).toBe('dark')
    expect(localStorage.getItem(THEME_KEY)).toBe('dark')
    expect(screen.getByRole('status')).toHaveTextContent('Theme: dark')
    fireEvent.click(button())
    expect(button()).toHaveAccessibleName('Theme: light')
    expect(document.documentElement.dataset.theme).toBe('light')
    fireEvent.click(button())
    expect(document.documentElement.dataset.theme).toBeUndefined()
    expect(localStorage.getItem(THEME_KEY)).toBeNull()
  })

  it('starts from the theme the inline script applied', () => {
    document.documentElement.dataset.theme = 'dark'
    render(<ThemeToggle />)
    expect(screen.getByRole('button')).toHaveAccessibleName('Theme: dark')
  })

  it('still switches when storage refuses', () => {
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('denied')
    })
    render(<ThemeToggle />)
    fireEvent.click(screen.getByRole('button'))
    expect(document.documentElement.dataset.theme).toBe('dark')
  })
})

// Both entry pages pinned data-theme="dark" before #1355. A pin, or a theme
// script that runs after the styles load, flashes or locks the wrong theme.
describe.each([['index.html', indexHtml], ['__mock.html', mockHtml]])('%s', (_, html) => {
  it('does not pin a theme', () => {
    expect(html).not.toMatch(/<html[^>]*\sdata-theme=/)
  })
  it('applies a saved theme before any script or stylesheet loads', () => {
    const at = html.indexOf("localStorage.getItem('hydra-theme')")
    expect(at).toBeGreaterThan(-1)
    expect(at).toBeLessThan(html.search(/<script type="module"|<link rel="stylesheet"/))
  })
})
