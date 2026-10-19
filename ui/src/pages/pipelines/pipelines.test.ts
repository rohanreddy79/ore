import { describe, expect, it } from 'vitest'
import { findFailure, searchLines } from './LogViewer'
import { layerTasks } from './Run'

describe('log helpers', () => {
  const lines = Array.from({ length: 50_000 }, (_, i) => (i === 42_000 ? 'FAIL: TestGreeting (0.00s)' : `line ${i} ok`))
  it('finds the first failure in a 50,000-line log quickly', () => {
    const t0 = performance.now()
    expect(findFailure(lines)).toBe(42_000)
    expect(findFailure(['all good', 'exit status 0'])).toBe(-1)
    expect(findFailure(['panic: boom'])).toBe(0)
    expect(performance.now() - t0).toBeLessThan(300)
  })
  it('searches case-insensitively', () => {
    const t0 = performance.now()
    expect(searchLines(lines, 'testgreeting')).toEqual([42_000])
    expect(searchLines(lines, '')).toEqual([])
    expect(searchLines(lines, 'line 49999')).toEqual([49_999])
    expect(performance.now() - t0).toBeLessThan(300)
  })
})

describe('layerTasks', () => {
  it('layers by needs and tolerates cycles and unknown deps', () => {
    const layers = layerTasks([
      { id: 'build' },
      { id: 'test', needs: ['build'] },
      { id: 'lint', needs: ['build'] },
      { id: 'release', needs: ['test', 'lint'] },
      { id: 'orphan', needs: ['missing'] },
    ])
    expect(layers.map((l) => l.map((t) => t.id))).toEqual([['build', 'orphan'], ['test', 'lint'], ['release']])
    const cyc = layerTasks([{ id: 'a', needs: ['b'] }, { id: 'b', needs: ['a'] }])
    expect(cyc.flat().map((t) => t.id).sort()).toEqual(['a', 'b'])
  })
})
