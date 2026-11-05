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
      test(`axe at ${name} width`, async ({ page }) => {
        await page.setViewportSize(viewport)
        await page.goto(path)
        // let scroll-reveal fades finish, or axe measures a half-faded paragraph
        await page.waitForTimeout(800)
        const { violations } = await new AxeBuilder({ page }).analyze()
        const summary = violations.map((v) => `${v.id} (${v.impact}) x${v.nodes.length}: ${v.nodes[0].target.join(' ')}`)
        expect(summary).toEqual([])
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
      const over = [...document.querySelectorAll('.hero-sticky *')]
        .filter((e) => !e.closest('.hero-inner') && e.checkVisibility() && getComputedStyle(e).position === 'absolute'
          && !['CANVAS'].includes(e.tagName) && getComputedStyle(e).pointerEvents !== 'none' || e.matches('.hud-panel, .deep'))
        .filter((e) => e.checkVisibility() && parseFloat(getComputedStyle(e).opacity) > 0)
      const out = []
      for (const o of over) for (const t of text) if (meet(box(o), box(t))) out.push(`${o.className} over ${t.tagName}.${t.className}`)
      return out
    })
    expect(hits, `at ${width}px`).toEqual([])
  }
})
