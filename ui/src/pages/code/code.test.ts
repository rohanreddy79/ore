import { describe, expect, it } from 'vitest'
import { buildTree, findNode, fuzzyScore, sortedChildren } from './Code'
import { parseLineHash } from './FileView'

describe('file tree', () => {
  const root = buildTree(['README.md', 'src/a.ts', 'src/b/c.ts', 'docs/x.md'])
  it('builds nested nodes with directories first', () => {
    expect(sortedChildren(root).map((n) => [n.name, n.dir])).toEqual([
      ['docs', true],
      ['src', true],
      ['README.md', false],
    ])
    expect(findNode(root, 'src/b')?.children.has('c.ts')).toBe(true)
    expect(findNode(root, 'nope')).toBeNull()
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
