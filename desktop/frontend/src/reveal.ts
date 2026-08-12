// Shared entrance-animation primitives for the Dashboard's HUD treatment:
// arc gauges sweeping in, bars growing from a zero baseline, numbers counting
// up. All three need the same two things — "has real data arrived" and
// "should motion play at all" — so they live here once instead of three
// times.

import { useEffect, useRef, useState } from 'react'

export function prefersReducedMotion(): boolean {
  return typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches
}

/**
 * Flips from false to true once, the first time `ready` becomes true —
 * never again after, so a 5s dashboard poll that keeps `ready` true doesn't
 * replay the entrance on every refresh. Two rAFs after mount so the browser
 * paints the pre-reveal state first; a CSS transition needs that or it never
 * animates. Skips straight to true under reduced motion — the caller's CSS
 * still owns removing the transition itself.
 */
export function useReveal(ready: boolean): boolean {
  const [revealed, setRevealed] = useState(false)
  const firedRef = useRef(false)

  useEffect(() => {
    if (!ready || firedRef.current) return
    firedRef.current = true
    if (prefersReducedMotion()) {
      setRevealed(true)
      return
    }
    const raf1 = requestAnimationFrame(() => {
      requestAnimationFrame(() => setRevealed(true))
    })
    return () => cancelAnimationFrame(raf1)
  }, [ready])

  return revealed
}

/** Eases 0→1 like the reveal sweep (cubic ease-out), for count-up numbers. */
function easeOutCubic(p: number): number {
  return 1 - Math.pow(1 - p, 3)
}

/**
 * Counts up to `target` the first time `revealed` flips true. Reports 0
 * before that, so a stat card never flashes its final value ahead of the
 * reveal. Later changes to `target` (the next 5s poll landing a new number)
 * snap straight to the new value instead of re-running the count-up — the
 * animation is an arrival effect, not something to replay on every refresh.
 */
export function useCountUp(target: number, revealed: boolean, durationMs = 900): number {
  const [value, setValue] = useState(0)
  const animatedRef = useRef(false)

  useEffect(() => {
    if (!revealed) return
    if (animatedRef.current || prefersReducedMotion()) {
      animatedRef.current = true
      setValue(target)
      return
    }
    animatedRef.current = true
    let raf = 0
    const t0 = performance.now()
    const step = (t: number) => {
      const p = Math.min(1, (t - t0) / durationMs)
      setValue(target * easeOutCubic(p))
      if (p < 1) raf = requestAnimationFrame(step)
    }
    raf = requestAnimationFrame(step)
    return () => cancelAnimationFrame(raf)
  }, [revealed, target, durationMs])

  return value
}
