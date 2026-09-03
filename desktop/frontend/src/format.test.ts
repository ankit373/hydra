import { describe, expect, it } from 'vitest'
import { calibrationLabel, calibrationStrength, calibrationWidthPct, costBand } from './format'

describe('costBand does not alarm about sub-cent spend', () => {
  // The real defect (#594): $0.0009 was the largest figure in the table, so
  // share-of-max alone painted a tenth of a cent red and $0.0000 green.
  it('does not call a tenth of a cent expensive, even as the set maximum', () => {
    expect(costBand(0.0009, 0.0009)).toBe('cheap')
  })

  it('holds the whole sub-cent range at cheap', () => {
    for (const v of [0.0001, 0.0009, 0.002, 0.0099]) {
      expect(costBand(v, 0.0099)).toBe('cheap')
    }
  })

  it('still reports free for nothing spent', () => {
    expect(costBand(0, 0.0009)).toBe('free')
  })

  it('keeps the share ramp once the amounts are real', () => {
    expect(costBand(1, 1)).toBe('expensive')
    expect(costBand(0.7, 1)).toBe('expensive')
    expect(costBand(0.3, 1)).toBe('mid')
    expect(costBand(0.05, 1)).toBe('cheap')
  })

  // Security's CountBars/ByHead reuse this ramp for integer counts, where any
  // non-zero row is already above a floor expressed in dollars.
  it('leaves the integer-count reuse untouched', () => {
    expect(costBand(5, 5)).toBe('expensive')
    expect(costBand(2, 5)).toBe('mid')
    expect(costBand(1, 9)).toBe('cheap')
  })
})

describe('calibration bars are on an absolute scale', () => {
  // The real defect (#593): every top row sat at D=0.28 nats and, normalised
  // against the set, drew as a full bar. D=0 is a coin flip.
  it('draws D=0.28 nats as a sliver, not a full bar', () => {
    const w = calibrationWidthPct(0.28, 40)
    expect(w).toBeGreaterThan(0)
    expect(w).toBeLessThan(15)
  })

  it('is independent of the other rows', () => {
    expect(calibrationWidthPct(0.28, 40)).toBe(calibrationWidthPct(0.28, 4000))
  })

  it('never exceeds the track, however diagnostic the source', () => {
    for (const d of [2.9, 3, 12, 1e6]) expect(calibrationWidthPct(d, 100)).toBeLessThanOrEqual(100)
    expect(calibrationWidthPct(1e6, 100)).toBe(100)
  })

  it('reaches full width only at the LLR that buys 95% on its own', () => {
    expect(calibrationWidthPct(Math.log(0.95 / 0.05), 100)).toBeCloseTo(100, 6)
    expect(calibrationWidthPct(1, 100)).toBeCloseTo(33.96, 1)
  })

  it('keeps a sliver for a measured coin flip, and nothing for no data', () => {
    expect(calibrationWidthPct(0, 12)).toBe(2)
    expect(calibrationWidthPct(0, 0)).toBe(0)
  })

  it('rises with D', () => {
    const ws = [0.1, 0.5, 1, 2].map((d) => calibrationWidthPct(d, 100))
    expect(ws).toEqual([...ws].sort((a, b) => a - b))
    expect(new Set(ws).size).toBe(4)
  })
})

describe('calibration strength says in words what the nats mean', () => {
  it('bands on the thresholds the Cockpit already used', () => {
    expect(calibrationStrength(0.28, 40)).toBe('weak')
    expect(calibrationStrength(0.49, 40)).toBe('weak')
    expect(calibrationStrength(0.5, 40)).toBe('moderate')
    expect(calibrationStrength(0.99, 40)).toBe('moderate')
    expect(calibrationStrength(1, 40)).toBe('strong')
  })

  it('calls thin evidence thin whatever D says', () => {
    expect(calibrationStrength(4, 9)).toBe('thin')
    expect(calibrationStrength(4, 10)).toBe('strong')
  })

  it('reads as plain language', () => {
    expect(calibrationLabel('weak')).toBe('weak evidence')
    expect(calibrationLabel('thin')).toBe('too few samples')
  })
})
