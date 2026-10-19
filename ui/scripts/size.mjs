// Prints gzipped sizes of the built assets and checks the shell budget: the
// shell is everything index.html loads up front (entry, preloaded vendor
// chunks and the stylesheet); route and grammar chunks load on demand.
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { gzipSync } from 'node:zlib'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

const dist = fileURLToPath(new URL('../../internal/webapp/dist/', import.meta.url))
const html = readFileSync(join(dist, 'index.html'), 'utf8')
const shellFiles = new Set([...html.matchAll(/\/app\/assets\/([^"']+)/g)].map((m) => m[1]))
const assets = join(dist, 'assets')
const rows = []
let shell = 0
for (const f of readdirSync(assets)) {
  const p = join(assets, f)
  if (!statSync(p).isFile()) continue
  const gz = gzipSync(readFileSync(p)).length
  const isShell = shellFiles.has(f)
  if (isShell) shell += gz
  rows.push([f, gz, isShell ? 'shell' : 'lazy'])
}
rows.sort((a, b) => b[1] - a[1])
for (const [f, gz, kind] of rows.slice(0, 25)) console.log(`${String((gz / 1024).toFixed(1)).padStart(8)} KB  ${kind.padEnd(6)} ${f}`)
console.log(`\nshell (what index.html loads up front), gzipped: ${(shell / 1024).toFixed(1)} KB of a 350 KB budget; ${rows.length} assets in total`)
if (shell > 350 * 1024) {
  console.error('shell exceeds the 350 KB budget')
  process.exit(1)
}
