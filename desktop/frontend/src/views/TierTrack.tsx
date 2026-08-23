import type { TimelineEntry } from '../types'

/**
 * Fewest tiers of vertical scale to draw, so a single one-tier hop does not
 * fill the whole height and read as a dramatic swing.
 */
const MIN_SPAN = 3

/**
 * How the router moved through tiers across one run.
 *
 * Answers the question the Timeline cannot: did this escalate, or go straight to
 * the tier it needed? The Timeline tags every row with its tier, but reading a
 * *sequence* out of a list of rows is exactly the work this saves.
 *
 * Stronger tiers sit higher, because tier 1 is the strongest and a line that
 * climbed reads as "this got harder" without a legend.
 */
export function TierTrack({ entries }: { entries: TimelineEntry[] }) {
  const steps = tierSteps(entries)
  if (steps === null) return null

  const w = 100 // viewBox units; the SVG scales to its container
  const h = 44
  const pad = 7

  // Scaled to the tiers this run actually used, not the full 1-10. Across a
  // wide column the full range flattens a real escalation into a near-straight
  // line. The trade-off: two runs' tracks are not comparable by height, which
  // is why every point is labelled with its own tier below.
  const tiers = steps.map((s) => s.tier)
  const top = Math.min(...tiers)
  const span = Math.max(Math.max(...tiers) - top, MIN_SPAN)
  // Lower tier number is stronger, so it sits higher.
  const y = (tier: number) => pad + ((tier - top) / span) * (h - pad * 2)
  const x = (i: number) => (steps.length === 1 ? w / 2 : (i / (steps.length - 1)) * w)

  // A step path, not a straight interpolation: the router held a tier and then
  // jumped. Drawing a diagonal would imply it passed through the tiers between.
  const d = steps
    .map((s, i) => {
      const px = x(i)
      const py = y(s.tier)
      if (i === 0) return `M ${px} ${py}`
      return `L ${px} ${y(steps[i - 1].tier)} L ${px} ${py}`
    })
    .join(' ')

  return (
    <div className="tt">
      <div className="tt__head">
        <span className="tt__lbl">Tier movement</span>
        <span className="tt__sum">{summary(steps)}</span>
      </div>
      {/* The line stretches with the column, so the SVG is non-uniformly
          scaled. Circles cannot live in there — X scales ~10x and Y 1x, which
          renders every dot as a wide smear. They go on top as HTML, positioned
          in percent, so they stay round at any width. */}
      <div className="tt__plot" role="img" aria-label={ariaLabel(steps)}>
        <svg className="tt__svg" viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none" aria-hidden="true">
          <path d={d} className="tt__line" />
        </svg>
        {steps.map((s, i) => (
          <span
            key={i}
            className={`tt__dot tt__dot--${dir(steps, i)}`}
            style={{ left: `${x(i)}%`, top: `${y(s.tier)}px` }}
          />
        ))}
      </div>
      <div className="tt__axis">
        {steps.map((s, i) => (
          <span key={i} className={`tt__tick tt__tick--${dir(steps, i)}`}>
            T{s.tier}
          </span>
        ))}
      </div>
    </div>
  )
}

interface Step {
  tier: number
  /** How many timeline entries ran at this tier before it moved. */
  held: number
}

/**
 * The tier sequence, or null when a track would mislead rather than inform.
 *
 * Returns null for three distinct reasons, all deliberate:
 *  - an SPRT ensemble (`sample`) or swarm (`attempt`) run, where heads run in
 *    parallel: a step line implies the router moved through tiers in order, and
 *    drawing parallel attempts that way would state something false. Those need
 *    their own summary, which is a separate piece of work.
 *  - fewer than two tiers: nothing moved, and a flat line is noise. Same
 *    reasoning Session already uses to hide its Graph tab on a linear run.
 *  - no tier data at all.
 */
export function tierSteps(entries: TimelineEntry[]): Step[] | null {
  if (entries.some((e) => e.kind === 'sample' || e.kind === 'attempt')) return null

  const steps: Step[] = []
  for (const e of entries) {
    if (!e.tier || e.tier <= 0) continue
    const last = steps[steps.length - 1]
    if (last && last.tier === e.tier) {
      last.held++
      continue
    }
    steps.push({ tier: e.tier, held: 1 })
  }
  return steps.length >= 2 ? steps : null
}

/** Whether this step was reached by escalating, easing off, or is the start. */
function dir(steps: Step[], i: number): 'start' | 'up' | 'down' {
  if (i === 0) return 'start'
  // Lower tier number is stronger, so a decrease is an escalation.
  return steps[i].tier < steps[i - 1].tier ? 'up' : 'down'
}

function summary(steps: Step[]): string {
  const first = steps[0].tier
  const last = steps[steps.length - 1].tier
  const hops = steps.length - 1
  const word = hops === 1 ? 'once' : `${hops} times`
  if (last < first) return `escalated ${word} · T${first} → T${last}`
  if (last > first) return `eased off ${word} · T${first} → T${last}`
  return `moved ${word}, ended where it started · T${first}`
}

function ariaLabel(steps: Step[]): string {
  return `Tier movement: ${steps.map((s) => `T${s.tier}`).join(' then ')}`
}
