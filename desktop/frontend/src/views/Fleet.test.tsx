import { describe, expect, it } from 'vitest'
import { activitySummary, groupRuns } from './Fleet'
import type { Fleet as FleetData, Run } from '../types'

function run(over: Partial<Run> = {}): Run {
  return {
    id: '20260904T100000Z-a',
    live: false,
    waiting: false,
    startedAt: '',
    elapsedMs: 0,
    costUsd: 0,
    confidence: 0,
    agents: [],
    running: 0,
    ok: 1,
    failed: 0,
    pending: 0,
    allCount: 1,
    skipped: 0,
    goal: 'ship the thing',
    ...over,
  }
}

function fleet(over: Partial<FleetData> = {}): FleetData {
  return { hasRuns: true, liveCount: 0, waitingCount: 0, runs: [], groupThreshold: 8, ...over }
}

describe('grouping by who has to act', () => {
  it('puts what needs a person before what the machine is doing', () => {
    const groups = groupRuns([
      run({ id: 'done-1' }),
      run({ id: 'live-1', live: true }),
      run({ id: 'wait-1', waiting: true }),
      run({ id: 'fail-1', failed: 2, ok: 0 }),
    ])
    expect(groups.map((g) => g.id)).toEqual(['waiting', 'running', 'attention', 'done'])
  })

  it('leaves out a group with nothing in it', () => {
    const groups = groupRuns([run({ id: 'done-1' }), run({ id: 'live-1', live: true })])
    expect(groups.map((g) => g.id)).toEqual(['running', 'done'])
  })

  // A parked run is also not live, and a live run can already have a failed
  // agent while still working, so the branches must not double-count.
  it('classifies each run exactly once', () => {
    const groups = groupRuns([
      run({ id: 'a', waiting: true, failed: 3 }),
      run({ id: 'b', live: true, failed: 1 }),
    ])
    expect(groups.find((g) => g.id === 'waiting')?.runs.map((r) => r.id)).toEqual(['a'])
    expect(groups.find((g) => g.id === 'running')?.runs.map((r) => r.id)).toEqual(['b'])
    expect(groups.find((g) => g.id === 'attention')).toBeUndefined()
    const total = groups.reduce((n, g) => n + g.runs.length, 0)
    expect(total).toBe(2)
  })

  // A lone "Done" heading over every run a machine ever made labels nothing.
  it('does not head a single group', () => {
    const groups = groupRuns([run({ id: 'a' }), run({ id: 'b' })])
    expect(groups).toHaveLength(1)
    expect(groups[0].headed).toBe(false)
  })

  it('heads the groups once there is more than one to tell apart', () => {
    const groups = groupRuns([run({ id: 'a' }), run({ id: 'b', waiting: true })])
    expect(groups.every((g) => g.headed)).toBe(true)
  })

  it('has nothing to group when nothing has run', () => {
    expect(groupRuns([])).toEqual([])
  })
})

describe('the summary line leads with what needs a person', () => {
  it('says so plainly when nothing needs you', () => {
    expect(activitySummary(fleet())).toMatch(/nothing needs you/i)
  })

  it('names the waiting count before the running one', () => {
    const s = activitySummary(fleet({ waitingCount: 2, liveCount: 3 }))
    expect(s.indexOf('waiting')).toBeLessThan(s.indexOf('running'))
  })

  it('reads as one request in the singular', () => {
    expect(activitySummary(fleet({ waitingCount: 1 }))).toBe('1 request needs an answer from you')
  })

  it('falls back to running when nothing is parked', () => {
    expect(activitySummary(fleet({ liveCount: 1 }))).toBe('1 request running now')
  })
})
