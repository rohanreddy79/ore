import { describe, expect, it } from 'vitest'
import { fuzzyScore } from './Code'
import { parseLineHash } from './FileView'
import { ancestorsOf, parseChildren } from './tree'

describe('code helpers', () => {
  it('parses a depth-1 listing into sorted children, directories first', () => {
    expect(parseChildren(['src/b.ts', 'src/', 'README.md', 'docs/', 'a.txt']).map((c) => [c.name, c.path, c.dir])).toEqual([
      ['docs', 'docs', true],
      ['src', 'src', true],
      ['a.txt', 'a.txt', false],
      ['b.ts', 'src/b.ts', false],
      ['README.md', 'README.md', false],
    ])
  })
  it('lists the ancestors of a path', () => {
    expect(ancestorsOf('a/b/c')).toEqual(['a', 'a/b'])
    expect(ancestorsOf('a')).toEqual([])
    expect(ancestorsOf('')).toEqual([])
  })
  it('fuzzy scores paths', () => {
    expect(fuzzyScore('sat', 'src/a.ts')).not.toBeNull()
    expect(fuzzyScore('sac', 'src/a.ts')).toBeNull()
    expect(fuzzyScore('zz', 'src/a.ts')).toBeNull()
    expect(fuzzyScore('read', 'README.md')!).toBeLessThan(fuzzyScore('read', 'docs/readme-ish.md')!)
  })
  it('parses line hashes', () => {
    expect(parseLineHash('#L12')).toEqual([12, 12])
    expect(parseLineHash('#L12-L20')).toEqual([12, 20])
    expect(parseLineHash('#L20-12')).toEqual([12, 20])
    expect(parseLineHash('#foo')).toBeNull()
  })
})
