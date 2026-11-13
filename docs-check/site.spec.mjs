// Every page of the website, in a real browser: accessibility, layout and runtime errors.
// Asserts properties, never pixels, so copy and spacing can change without touching it.
import { test, expect } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { readdirSync } from 'node:fs'

// Every page under docs/, found rather than listed, so a new page is checked from day one.
const DOCS = new URL('../docs/', import.meta.url)
const PAGES = ['', 'blog/'].flatMap((dir) => readdirSync(new URL(dir, DOCS))
  .filter((f) => f.endsWith('.html') && !f.startsWith('google')).map((f) => dir + f))
const WIDTHS = [320, 390, 768, 1024, 1180, 1440]
const PHONE = { width: 390, height: 844 }
const DESKTOP = { width: 1440, height: 900 }

// Third-party analytics is not part of the site; answer it locally so a CI runner's
// network cannot turn into a console error.
test.beforeEach(async ({ page }) => {
  await page.route(/^https?:\/\/(?!127\.0\.0\.1)/, (r) => r.fulfill({ status: 200, body: '' }))
})

function collectErrors(page) {
  const errors = []
  page.on('pageerror', (e) => errors.push(`pageerror: ${e.message}`))
  page.on('console', (m) => { if (m.type() === 'error') errors.push(`console: ${m.text()}`) })
  return errors
}

for (const path of PAGES) {
  test.describe(path, () => {
    for (const width of WIDTHS) {
      test(`layout at ${width}px`, async ({ page }) => {
        const errors = collectErrors(page)
        await page.setViewportSize({ width, height: 900 })
        await page.goto(path)
        await page.waitForTimeout(800) // let a few animation frames run
        const r = await page.evaluate(() => {
          const vw = document.documentElement.clientWidth
          const small = []
          for (const e of document.querySelectorAll('body *')) {
            if (!e.checkVisibility()) continue
            const own = [...e.childNodes].some((n) => n.nodeType === 3 && n.textContent.trim())
            if (!own) continue
            const px = parseFloat(getComputedStyle(e).fontSize)
            if (px < 12) small.push(`${e.tagName.toLowerCase()}.${e.getAttribute('class') || ''} ${px}px "${e.textContent.trim().slice(0, 30)}"`)
          }
          return { scroll: document.documentElement.scrollWidth, vw, small: small.slice(0, 10) }
        })
        expect(r.scroll, 'the page scrolls sideways').toBeLessThanOrEqual(r.vw)
        expect(r.small, 'text under the 12px floor').toEqual([])
        expect(errors).toEqual([])
      })
    }

    for (const [name, viewport] of [['phone', PHONE], ['desktop', DESKTOP]]) {
      // The phone is a touch device, so (hover: none) styles apply: a control shown
      // only on hover elsewhere is always visible there, and axe must see it (#1355).
      test.describe(() => {
        if (name === 'phone') test.use({ isMobile: true, hasTouch: true })
        test(`axe at ${name} width @themed`, async ({ page }) => {
          await page.setViewportSize(viewport)
          await page.goto(path)
          // let scroll-reveal fades finish, or axe measures a half-faded paragraph
          await page.waitForTimeout(800)
          const { violations } = await new AxeBuilder({ page }).analyze()
          const summary = violations.map((v) => `${v.id} (${v.impact}) x${v.nodes.length}: ${v.nodes[0].target.join(' ')}`)
          expect(summary).toEqual([])
        })
      })
    }

    test('header: skip link, main landmark and the app call to action on a phone', async ({ page }) => {
      await page.setViewportSize({ width: 320, height: 700 })
      await page.goto(path)
      await expect(page.locator('main#main')).toHaveCount(1)
      await page.keyboard.press('Tab')
      await expect(page.locator('a.skip-link')).toBeFocused()
      await expect(page.locator('.site-header .header-cta')).toBeInViewport()

      const toggle = page.locator('.site-header .nav-toggle')
      await expect(toggle).toHaveAttribute('aria-expanded', 'false')
      const docs = page.locator('#site-nav a[href$="docs.html"]')
      await expect(docs).toBeHidden()
      await toggle.click()
      await expect(toggle).toHaveAttribute('aria-expanded', 'true')
      await expect(docs).toBeVisible()
      await page.keyboard.press('Tab') // the links it opened come next, not the page below
      await expect(page.locator('#site-nav a').first()).toBeFocused()
      await page.keyboard.press('Escape')
      await expect(toggle).toHaveAttribute('aria-expanded', 'false')
      await expect(toggle).toBeFocused()
    })

    test('no endless animation under reduced motion', async ({ page }) => {
      await page.emulateMedia({ reducedMotion: 'reduce' })
      await page.goto(path)
      await page.waitForTimeout(300)
      const endless = await page.evaluate(() => document.getAnimations()
        .filter((a) => a.playState === 'running' && a.effect.getTiming().iterations === Infinity)
        .map((a) => a.animationName || a.effect.target?.className || 'animation'))
      expect(endless).toEqual([])
    })
  })
}

// The docs mascot is display:none on phones; its loop used to keep drawing into a
// 0x0 canvas and throw on every frame (WEB-40).
test('docs mascot loop stops while it cannot be seen', async ({ page }) => {
  await page.addInitScript(() => {
    const raf = window.requestAnimationFrame.bind(window)
    window.__frames = 0
    window.requestAnimationFrame = (cb) => raf((t) => { window.__frames++; cb(t) })
  })
  const errors = collectErrors(page)
  await page.setViewportSize(PHONE)
  await page.goto('docs.html')
  await page.waitForTimeout(500)
  const before = await page.evaluate(() => window.__frames)
  await page.waitForTimeout(1000)
  const after = await page.evaluate(() => window.__frames)
  expect(after - before, 'frames drawn while the mascot is hidden').toBe(0)
  expect(errors).toEqual([])
})

// The hero's overlays must never sit on the hero's own words (WEB-21).
test('nothing in the home hero covers its text', async ({ page }) => {
  for (const width of WIDTHS) {
    await page.setViewportSize({ width, height: 800 })
    await page.goto('index.html')
    const hits = await page.evaluate(() => {
      const box = (e) => e.getBoundingClientRect()
      const meet = (a, b) => a.left < b.right && b.left < a.right && a.top < b.bottom && b.top < a.bottom
      const text = [...document.querySelectorAll('.hero-inner *')].filter((e) => e.checkVisibility() && box(e).width)
      // Overlays: any text the hero draws outside its own copy, while it can be seen.
      const seen = (e) => { for (let a = e; a; a = a.parentElement) if (getComputedStyle(a).opacity === '0') return false; return true }
      const over = [...document.querySelectorAll('.hero-sticky *')].filter((e) => !e.closest('.hero-inner')
        && [...e.childNodes].some((n) => n.nodeType === 3 && n.textContent.trim())
        && e.checkVisibility() && seen(e) && box(e).width > 0)
      const out = []
      for (const o of over) for (const t of text) if (meet(box(o), box(t))) out.push(`${o.className} over ${t.tagName}.${t.className}`)
      return out
    })
    expect(hits, `at ${width}px`).toEqual([])
  }
})

// Without script nothing may hide: the nav wraps instead of folding, and no section waits to fade in.
test.describe('without JavaScript', () => {
  test.use({ javaScriptEnabled: false })
  for (const path of PAGES) {
    test(`${path} is readable and navigable at 390px`, async ({ page }) => {
      await page.setViewportSize(PHONE)
      await page.goto(path)
      await expect(page.locator('#site-nav a[href$="docs.html"]')).toBeVisible()
      const hidden = await page.evaluate(() => [...document.querySelectorAll('.reveal')]
        .filter((e) => getComputedStyle(e).opacity === '0').map((e) => e.id || e.className))
      expect(hidden).toEqual([])
      const r = await page.evaluate(() => [document.documentElement.scrollWidth, document.documentElement.clientWidth])
      expect(r[0]).toBeLessThanOrEqual(r[1])
      // site.js gives an overflowing region its tab stop; without it, nothing may scroll sideways (WCAG 2.1.1).
      for (const width of [PHONE.width, DESKTOP.width]) {
        await page.setViewportSize({ width, height: 900 })
        const stuck = await page.evaluate(() => [...document.querySelectorAll('main *')].filter((e) =>
          /auto|scroll/.test(getComputedStyle(e).overflowX) && e.scrollWidth > e.clientWidth + 1 && !e.hasAttribute('tabindex'))
          .map((e) => `${e.tagName.toLowerCase()}.${e.className}`))
        expect(stuck, `at ${width}px`).toEqual([])
      }
    })
  }
})

// The theme follows the OS until the viewer picks one, and the pick survives a reload (#1355).
test.describe('theme', () => {
  test.skip(({ colorScheme }) => colorScheme !== 'light', 'drives the theme itself')

  test('the toggle is a labelled button a keyboard can cycle, and the choice persists', async ({ page }) => {
    await page.setViewportSize({ width: 320, height: 700 })
    await page.goto('index.html')
    const toggle = page.locator('.site-header .theme-toggle')
    await expect(toggle).toBeInViewport()
    await expect(toggle).toHaveAccessibleName('Theme: match system')
    const base = () => page.evaluate(() => getComputedStyle(document.body).backgroundColor)
    const light = await base()
    // The OS is light here, so the first press must go to dark, or it changes nothing on screen.
    await toggle.focus()
    await page.keyboard.press('Enter')
    await expect(toggle).toHaveAccessibleName('Theme: dark')
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
    expect(await base()).not.toBe(light)
    await page.reload()
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
    await expect(toggle).toHaveAccessibleName('Theme: dark')
    await toggle.click()
    await expect(toggle).toHaveAccessibleName('Theme: light')
    await toggle.click()
    await expect(toggle).toHaveAccessibleName('Theme: match system')
    await expect(page.locator('html')).not.toHaveAttribute('data-theme', /./)
    expect(await base()).toBe(light)
  })

  // A theme set after a stylesheet is parsed can paint the other theme first.
  test('a saved choice is in place before any stylesheet loads', async ({ page }) => {
    await page.addInitScript(() => {
      localStorage.setItem('hydra-theme', 'dark')
      new MutationObserver(() => { window.__sheetsAtTheme ??= document.styleSheets.length })
        .observe(document, { subtree: true, attributeFilter: ['data-theme'] })
    })
    for (const path of PAGES) {
      await page.goto(path)
      expect(await page.evaluate(() => window.__sheetsAtTheme), `${path}: stylesheets parsed before the theme was set`).toBe(0)
    }
  })

  // A still frame (reduced motion) is drawn once, so only a repaint on the theme
  // change can move its pixels; a canvas keeping stale colours would not.
  test('canvases repaint in the new theme', async ({ page }) => {
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await page.goto('pricing.html')
    await page.waitForTimeout(400)
    const pixels = () => page.evaluate(() => document.getElementById('aurora').toDataURL())
    const before = await pixels()
    const colour = await page.evaluate(() => Hydra.color('surface-base'))
    await page.locator('.theme-toggle').click() // the OS is light, so this is dark
    await page.waitForTimeout(400)
    expect(await page.evaluate(() => Hydra.color('surface-base'))).not.toBe(colour)
    expect(await pixels(), 'the aurora kept its light-theme frame').not.toBe(before)
  })

  test('with storage refused the page still loads and the toggle still works', async ({ page }) => {
    const errors = collectErrors(page)
    await page.addInitScript(() => Object.defineProperty(window, 'localStorage', { get() { throw new Error('denied') } }))
    await page.goto('index.html')
    await page.locator('.theme-toggle').click()
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
    expect(errors).toEqual([])
  })
})
