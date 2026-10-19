import { describe, expect, it } from 'vitest'
import { fmtBytes, fmtDuration, refName, shortId, timeAgo, tryJSON } from './format'

describe('format', () => {
  it('timeAgo', () => {
    const now = Date.parse('2026-10-19T12:00:00Z')
    expect(timeAgo('2026-10-19T11:59:50Z', now)).toBe('just now')
    expect(timeAgo('2026-10-19T11:30:00Z', now)).toMatch(/30 minutes ago/)
    expect(timeAgo('2026-10-17T12:00:00Z', now)).toMatch(/2 days ago/)
    expect(timeAgo('garbage', now)).toBe('garbage')
    expect(timeAgo(undefined, now)).toBe('')
  })
  it('fmtDuration and bytes', () => {
    expect(fmtDuration(12)).toBe('12 ms')
    expect(fmtDuration(1500)).toBe('1.5 s')
    expect(fmtDuration(90_000)).toBe('1m 30s')
    expect(fmtDuration(2 * 3600_000 + 60_000)).toBe('2h 1m')
    expect(fmtBytes(512)).toBe('512 B')
    expect(fmtBytes(2048)).toBe('2.0 KB')
  })
  it('refName and shortId', () => {
    expect(refName('refs/heads/feature/x')).toBe('feature/x')
    expect(refName('refs/tags/v1')).toBe('v1')
    expect(shortId('prun_000038AHYV8KJG08NNNJ')).toBe('prun_000038')
  })
  it('tryJSON', () => {
    expect(tryJSON('{"a":1}', {})).toEqual({ a: 1 })
    expect(tryJSON('nope', [])).toEqual([])
  })
})
