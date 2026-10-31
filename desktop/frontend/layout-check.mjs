// Layout regression check: the app rendered in a real browser, measured.
//
// jsdom has no layout, so vitest cannot see any of this. Three defects shipped
// in one day that only a browser could catch: a graph frame four times its
// content, a header that overflowed once the nav grew a sixth tab, and an
// eleven-tab strip whose last two tabs were simply unreachable. The header one
// was a regression against a width a previous PR had verified by hand.
//
// It asserts properties, never pixel values: nothing here breaks because a
// label was reworded or a margin changed.
import { chromium } from 'playwright'
import { spawn } from 'node:child_process'
import process from 'node:process'

const PORT = Number(process.env.LAYOUT_PORT || 5399)
const URL = `http://localhost:${PORT}/__mock.html`

// 980 is main.go's MinWidth, so the window cannot be dragged narrower. The rest
// are reachable by zooming in, which is what narrows the CSS viewport.
const WIDTHS = [1440, 980, 900, 820]

const failures = []
const fail = (where, msg) => failures.push(`${where}: ${msg}`)

/** Everything a person can click. What is off-screen here cannot be reached. */
const CONTROLS = 'button, [role="tab"], a[href]'

async function measure(page, where) {
  const r = await page.evaluate((sel) => {
    const vw = window.innerWidth
    const out = { overflow: null, unreachable: [], clipped: [] }
    // className is an SVGAnimatedString inside an <svg>, which stringifies to
    // "[object SVGAnimatedString]" and names nothing.
    const name = (e) => {
      const c = e.getAttribute && e.getAttribute('class')
      return (c || e.tagName).toString().trim().slice(0, 44)
    }

    if (document.documentElement.scrollWidth > vw + 1) {
      // Name the widest thing past the edge, or the message is unactionable.
      let worst = null, worstW = 0
      for (const e of document.querySelectorAll('*')) {
        const b = e.getBoundingClientRect()
        if (b.right > vw + 1 && b.width > worstW) {
          worstW = b.width
          worst = `${name(e)} right=${Math.round(b.right)}`
        }
      }
      out.overflow = `page scrolls horizontally (${document.documentElement.scrollWidth} > ${vw}); widest past the edge: ${worst}`
    }

    for (const e of document.querySelectorAll(sel)) {
      // checkVisibility covers display, visibility and content-visibility. A
      // control in a closed panel is not a layout defect.
      if (e.checkVisibility && !e.checkVisibility()) continue
      const b = e.getBoundingClientRect()
      if (b.width === 0 || b.height === 0) continue
      // Deliberately parked off-canvas (a slide-in drawer) rather than pushed
      // there by a layout that does not fit.
      let parked = false
      for (let n = e; n && n !== document.body; n = n.parentElement) {
        const t = getComputedStyle(n).transform
        if (t && t !== 'none') { parked = true; break }
      }
      if (parked) continue
      if (b.right > vw + 1 || b.left < -1) {
        const label = (e.textContent || '').trim().slice(0, 28) || name(e)
        out.unreachable.push(`${label} (right=${Math.round(b.right)})`)
      }
    }

    // Content wider than its box with nothing to scroll it: not merely cut
    // off, but impossible to bring into view.
    //
    // Only reported when the page itself fits. A page that overflows makes
    // every ancestor report the same number, and five restatements of one
    // fact are harder to act on than the one line above.
    if (!out.overflow) {
      for (const e of document.querySelectorAll('*')) {
        if (e.scrollWidth <= e.clientWidth + 1 || e.clientWidth === 0) continue
        if (getComputedStyle(e).overflowX !== 'visible') continue
        // An ancestor reporting the same clip is reporting its child's.
        const inner = [...e.querySelectorAll('*')].some(
          (k) => k.scrollWidth > k.clientWidth + 1 && getComputedStyle(k).overflowX === 'visible',
        )
        if (!inner) out.clipped.push(`${name(e)} ${e.scrollWidth}>${e.clientWidth}`)
      }
    }
    return out
  }, CONTROLS)

  if (r.overflow) fail(where, r.overflow)
  if (r.unreachable.length) fail(where, `off-screen and unclickable: ${r.unreachable.join(', ')}`)
  if (r.clipped.length) fail(where, `content cut off with no way to scroll it: ${r.clipped.join(', ')}`)
}

async function run() {
  const browser = await chromium.launch()
  for (const width of WIDTHS) {
    const page = await browser.newPage({ viewport: { width, height: 900 }, reducedMotion: 'reduce' })
    page.on('pageerror', (e) => fail(`${width}`, `page error: ${e.message}`))
    await page.goto(URL, { waitUntil: 'networkidle' })
    await page.waitForTimeout(700)

    for (const view of ['Chat', 'Models', 'Activity', 'Usage', 'Audit', 'Settings']) {
      await page.locator(`.apphead__tab:has-text("${view}")`).first().click()
      await page.waitForTimeout(600)
      await measure(page, `${width}/${view}`)

      // Audit's second strip is eleven tabs and is where #1125 hid two panels.
      if (view === 'Audit') {
        await page.locator('[role="tab"]:has-text("Detailed")').first().click()
        await page.waitForTimeout(600)
        await measure(page, `${width}/Audit:Detailed`)
      }
    }

    // Session is reached by opening a run, never from the nav, so a sweep of
    // the nav alone never renders it.
    await page.locator('.apphead__tab:has-text("Activity")').first().click()
    await page.waitForTimeout(500)
    await page.locator('.run__open').first().click()
    await page.waitForTimeout(700)
    for (const tab of ['Timeline', 'Code', 'Graph']) {
      await page.locator(`[role="tab"]:has-text("${tab}")`).first().click()
      await page.waitForTimeout(600)
      await measure(page, `${width}/Session:${tab}`)
    }
    await page.close()
  }
  await browser.close()
}

const vite = spawn('node_modules/.bin/vite', ['--port', String(PORT), '--strictPort'], {
  stdio: ['ignore', 'pipe', 'inherit'],
})
const ready = new Promise((resolve, reject) => {
  const t = setTimeout(() => reject(new Error('vite did not start in 30s')), 30_000)
  vite.stdout.on('data', (d) => {
    if (d.toString().includes('ready in')) { clearTimeout(t); resolve() }
  })
})

try {
  await ready
  await run()
} finally {
  vite.kill()
}

if (failures.length) {
  console.error(`\nLayout check failed, ${failures.length} finding${failures.length > 1 ? 's' : ''}:\n`)
  for (const f of failures) console.error(`  ${f}`)
  process.exit(1)
}
console.log(`Layout check clean: ${WIDTHS.length} widths x 10 surfaces.`)
