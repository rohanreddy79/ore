import { describe, expect, it } from 'vitest'
import { wordDiff } from './wordDiff'

describe('wordDiff', () => {
  it('marks only the changed words', () => {
    const { left, right } = wordDiff('return old(1, x);', 'return fresh(1, x); // changed')
    expect(left.filter((s) => s.changed).map((s) => s.text)).toEqual(['old'])
    expect(right.filter((s) => s.changed).map((s) => s.text).join('')).toContain('fresh')
  })
  it('gives up when nearly everything changed', () => {
    const { left, right } = wordDiff('alpha beta gamma', 'one two three four')
    expect(left.every((s) => !s.changed)).toBe(true)
    expect(right.every((s) => !s.changed)).toBe(true)
  })
  it('handles empty sides', () => {
    expect(wordDiff('', 'x').right[0]!.text).toBe('x')
  })
})
