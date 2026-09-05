// Display formatting. Kept separate from the views so it can be unit-tested,
// and so no component invents its own rounding.

/** Money, never rounded to a misleading $0.00 when the figure is merely small. */
export function usd(v: number): string {
  if (v === 0) return '$0'
  if (v < 0.01) return '<$0.01'
  return `$${v.toFixed(2)}`
}

/** Money at ledger precision, for tables where the exact figure matters. */
export function usdExact(v: number): string {
  if (v === 0) return '—'
  return `$${v.toFixed(4)}`
}

export function tokens(v: number): string {
  if (v >= 1_000_000) return `${(v / 1_000_000).toFixed(1)}M`
  if (v >= 1_000) return `${(v / 1_000).toFixed(1)}k`
  return String(v)
}

export function ms(v: number): string {
  if (v <= 0) return '—'
  if (v >= 1000) return `${(v / 1000).toFixed(1)}s`
  return `${v}ms`
}

export function pct(v: number, digits = 0): string {
  return `${v.toFixed(digits)}%`
}

/**
 * Governor band. Mirrors CLAUDE.md's claude_pct table, collapsed to the three
 * colours brand/tokens.css defines: 0–49 normal, 50–74 warning, 75+ critical.
 */
export function govBand(p: number): 'normal' | 'warning' | 'critical' {
  if (p >= 75) return 'critical'
  if (p >= 50) return 'warning'
  return 'normal'
}

/**
 * Coverage band for the OWASP LLM Top-10 score. Inverted from govBand: here
 * high is good, since this is "percent covered" not "percent of budget used".
 */
export function coverageBand(pct: number): 'good' | 'warn' | 'bad' {
  if (pct >= 80) return 'good'
  if (pct >= 50) return 'warn'
  return 'bad'
}

/**
 * The security category table as CSV, one row per applicable finding — the
 * same shape `hyctl security --csv` emits, so the two exports never disagree
 * about what a "finding" looks like.
 */
export function toSecurityCSV(
  categories: { id: string; name: string; status: string; gapAgeDays?: number; detail: string }[],
): string {
  const esc = (s: string) => `"${s.replace(/"/g, '""')}"`
  const rows = categories
    .filter((c) => c.status !== 'n/a')
    .map((c) => [c.id, esc(c.name), c.status, String(c.gapAgeDays ?? 0), esc(c.detail)].join(','))
  return ['id,name,status,gap_age_days,detail', ...rows].join('\n')
}

const COST_FLOOR_USD = 0.01

/** Cost ramp relative to the largest row, floored: sub-cent spend earns no
 * mid/expensive colour, because `usd()` already prints it "<$0.01" and the
 * biggest tenth of a cent is not expensive. */
export function costBand(v: number, max: number): 'free' | 'cheap' | 'mid' | 'expensive' {
  if (v <= 0) return 'free'
  if (max <= 0 || v < COST_FLOOR_USD) return 'cheap'
  const share = v / max
  if (share >= 0.6) return 'expensive'
  if (share >= 0.25) return 'mid'
  return 'cheap'
}

/** internal/trust's D is expected |LLR| in nats — 0 is a coin flip. ln(.95/.05)
 * is what one verdict must carry to move a 50/50 prior to the 95% the SPRT
 * ensemble targets, so it is the honest full scale for a bar. */
const CAL_FULL_SCALE_NATS = Math.log(0.95 / 0.05)

/** A source with observations but no diagnostic power keeps a sliver: the row
 * is real even when the measurement says coin flip. */
const CAL_SLIVER_PCT = 2

/** Absolute, never share-of-max: a leaderboard of weak sources must look weak.
 * At D=0.28 nats this is ~10% of the track, not the 100% a set-relative scale
 * gave the best of a bad field. */
export function calibrationWidthPct(d: number, n: number): number {
  if (d <= 0) return n > 0 ? CAL_SLIVER_PCT : 0
  return Math.min(100, Math.max(CAL_SLIVER_PCT, (d / CAL_FULL_SCALE_NATS) * 100))
}

export type CalStrength = 'thin' | 'weak' | 'moderate' | 'strong'

/** The bands Cockpit's per-model rows already read D by, in one place. n is
 * trust.Stat.N, which excludes the Laplace prior — under ten observations the
 * number is a guess whatever it says, so that outranks the value. */
export function calibrationStrength(d: number, n: number): CalStrength {
  if (n < 10) return 'thin'
  if (d >= 1) return 'strong'
  if (d >= 0.5) return 'moderate'
  return 'weak'
}

/** Plain words for the band. A nat figure is unreadable to anyone who has not
 * read internal/trust; the bar and the number both lean on this. */
export function calibrationLabel(s: CalStrength): string {
  if (s === 'thin') return 'too few samples'
  return `${s} evidence`
}

/** internal/trust calibration keys are "verifier:go-test" / "model:claude-sonnet"
 * — the prefix is the routing/storage key, not something worth showing next to
 * every row of a leaderboard. Falls back to the raw string for anything else. */
export function sourceLabel(source: string): string {
  const i = source.indexOf(':')
  return i < 0 ? source : source.slice(i + 1)
}

/** Oracles (verifier:*, e.g. test/lint results) are a structurally different
 * kind of evidence than a model's own self-report (model:*) — the calibration
 * leaderboard color-codes by this distinction. Unrecognized prefixes read as
 * 'model', the more common case. */
export function sourceKind(source: string): 'oracle' | 'model' {
  return source.startsWith('verifier:') ? 'oracle' : 'model'
}

/** "2026-08-02T10:04:11Z" → "10:04:11". Falls back to the raw string. */
export function clockTime(ts: string): string {
  const t = ts.indexOf('T')
  if (t < 0) return ts
  return ts.slice(t + 1, t + 9) || ts
}

/**
 * Updates of headroom before the orchestrator's context hits the 80% ceiling,
 * or null when there is no rate signal to project from.
 *
 * budget.RiskFromHistory returns zero below two observations, so a zero burn
 * rate means "never measured", not "not burning" — presenting that as headroom
 * would be a lie. Counts claude_pct *updates*, which are not chat turns and not
 * wall-clock time.
 */
export function contextHeadroom(g: {
  pct: number
  burnRatePct: number
  observations: number
}): number | null {
  if (g.observations < 2 || g.burnRatePct <= 0) return null
  return Math.max(0, Math.ceil((80 - g.pct) / g.burnRatePct))
}

/** What a context-budget mode means, for a reader who does not know the table. */
export function contextModeText(mode: string): string {
  switch (mode) {
    case 'normal':
      return 'plenty of room'
    case 'compact':
      return 'worth compacting soon'
    case 'caution':
      return 'compact now'
    case 'warning':
      return 'work is being downgraded a tier'
    case 'critical':
      return 'only the cheapest models from here'
    case 'emergency':
      return 'local models only'
    default:
      return mode
  }
}
