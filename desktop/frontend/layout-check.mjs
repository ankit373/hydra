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
import AxeBuilder from '@axe-core/playwright'
import { spawn } from 'node:child_process'
import process from 'node:process'

const PORT = Number(process.env.LAYOUT_PORT || 5399)
const URL = `http://localhost:${PORT}/__mock.html`

// 980 is main.go's MinWidth, so the window cannot be dragged narrower. The rest
// are reachable by zooming in, which is what narrows the CSS viewport.
const WIDTHS = [1440, 1280, 980, 900, 820]
const THEMES = ['light', 'dark']

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

// The thread is the job on Chat. Overflow checks cannot see a side panel that
// takes its width or is drawn over it, which is how #1431 shipped.
const CHAT_MIN = 560

async function chatFits(page, where) {
  const r = await page.evaluate(() => {
    const box = (sel) => {
      const e = document.querySelector(sel)
      if (!e || (e.checkVisibility && !e.checkVisibility())) return null
      return e.getBoundingClientRect()
    }
    const thread = box('.chatv')
    if (!thread) return { missing: true }
    const hits = []
    for (const sel of ['.chatside', '.cockpit']) {
      const b = box(sel)
      if (!b || b.width === 0) continue
      const x = Math.min(b.right, thread.right) - Math.max(b.left, thread.left)
      const y = Math.min(b.bottom, thread.bottom) - Math.max(b.top, thread.top)
      if (x > 1 && y > 1) hits.push(sel)
    }
    return { width: Math.round(thread.width), hits }
  })
  if (r.missing) return fail(where, 'chat thread not rendered')
  if (r.width < CHAT_MIN) fail(where, `chat thread is ${r.width}px wide, under ${CHAT_MIN}px`)
  if (r.hits.length) fail(where, `drawn over the chat thread: ${r.hits.join(', ')}`)
}

// Every surface the app can show, with the check run on each: the six nav views,
// Audit's detailed strip, and the Session tabs, which only opening a run reaches.
async function sweep(page, where, check) {
  for (const view of ['Chat', 'Models', 'Activity', 'Usage', 'Audit', 'Settings']) {
    await page.locator(`.apphead__tab:has-text("${view}")`).first().click()
    await page.waitForTimeout(600)
    await check(page, `${where}/${view}`)
    if (view === 'Chat' && check === measure) await chatFits(page, `${where}/Chat`)

    // Audit's second strip is eleven tabs and is where #1125 hid two panels.
    if (view === 'Audit') {
      await page.locator('[role="tab"]:has-text("Detailed")').first().click()
      await page.waitForTimeout(600)
      await check(page, `${where}/Audit:Detailed`)
    }
  }
  await page.locator('.apphead__tab:has-text("Activity")').first().click()
  await page.waitForTimeout(500)
  await page.locator('.run__open').first().click()
  await page.waitForTimeout(700)
  for (const tab of ['Timeline', 'Code', 'Graph']) {
    await page.locator(`[role="tab"]:has-text("${tab}")`).first().click()
    await page.waitForTimeout(600)
    await check(page, `${where}/Session:${tab}`)
  }
}

// Contrast and every other axe rule, in both themes (#1355): a colour that reads
// on one theme's surfaces is no evidence about the other's.
async function axe(page, where) {
  const { violations } = await new AxeBuilder({ page }).analyze()
  for (const v of violations) fail(where, `axe ${v.id} (${v.impact}) x${v.nodes.length}: ${v.nodes[0].target.join(' ')}`)
}

async function run() {
  const browser = await chromium.launch()
  for (const width of WIDTHS) {
    const page = await browser.newPage({ viewport: { width, height: 900 }, reducedMotion: 'reduce' })
    page.on('pageerror', (e) => fail(`${width}`, `page error: ${e.message}`))
    await page.goto(URL, { waitUntil: 'networkidle' })
    await page.waitForTimeout(700)
    await sweep(page, `${width}`, measure)
    await page.close()
  }
  for (const colorScheme of THEMES) {
    // axe needs a page it can open siblings beside, which browser.newPage() is not.
    const ctx = await browser.newContext({ viewport: { width: WIDTHS[0], height: 900 }, reducedMotion: 'reduce', colorScheme })
    const page = await ctx.newPage()
    page.on('pageerror', (e) => fail(colorScheme, `page error: ${e.message}`))
    await page.goto(URL, { waitUntil: 'networkidle' })
    await page.waitForTimeout(700)
    await sweep(page, colorScheme, axe)
    await ctx.close()
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
console.log(`Layout check clean: ${WIDTHS.length} widths x 10 surfaces, and axe clean on all 10 in light and dark.`)
