import { existsSync, readFileSync, rmSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = dirname(fileURLToPath(import.meta.url))

export default async function globalTeardown() {
  const p = join(__dirname, '.auth', 'server.json')
  if (!existsSync(p)) return
  const { pid, data } = JSON.parse(readFileSync(p, 'utf8')) as { pid: number; data: string }
  try {
    process.kill(-pid, 'SIGTERM')
  } catch {
    try {
      process.kill(pid, 'SIGTERM')
    } catch {
      /* already gone */
    }
  }
  if (!process.env.ORE_E2E_KEEP) rmSync(data, { recursive: true, force: true })
}
