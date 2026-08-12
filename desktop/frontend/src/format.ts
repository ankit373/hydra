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

/** Cost ramp for a per-row figure, relative to the largest row in its table. */
export function costBand(v: number, max: number): 'free' | 'cheap' | 'mid' | 'expensive' {
  if (v <= 0) return 'free'
  if (max <= 0) return 'cheap'
  const share = v / max
  if (share >= 0.6) return 'expensive'
  if (share >= 0.25) return 'mid'
  return 'cheap'
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
