import { spawn, execSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, writeFileSync, chmodSync, cpSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = dirname(fileURLToPath(import.meta.url))
import { chromium } from '@playwright/test'
import { BASE_URL, PORT, TOKEN } from '../playwright.config'
import { DEMO_FILES, api } from './api'

const root = resolve(__dirname, '..', '..')
const bin = join(root, 'bin', 'ore')

export default async function globalSetup() {
  if (!existsSync(bin)) {
    execSync('go build -trimpath -o bin/ore ./cmd/ore', { cwd: root, stdio: 'inherit', env: { ...process.env, CGO_ENABLED: '0' } })
  }
  const data = mkdtempSync(join(tmpdir(), 'ore-e2e-'))
  const agent = join(data, 'demo-agent.sh')
  cpSync(join(__dirname, 'fixtures', 'demo-agent.sh'), agent)
  chmodSync(agent, 0o755)
  const log = join(data, 'serve.log')
  const child = spawn(bin, ['serve', '--data', join(data, 'data'), '--addr', `127.0.0.1:${PORT}`, '--token', TOKEN, '--executor', 'host', '--agent-cmd', `sh ${agent}`, '--agent-name', 'demo-script', '--quiet'], {
    env: { ...process.env, ORE_SSH_ADDR: 'off' },
    stdio: ['ignore', 'pipe', 'pipe'],
    detached: true,
  })
  const chunks: string[] = []
  child.stdout?.on('data', (d) => chunks.push(String(d)))
  child.stderr?.on('data', (d) => chunks.push(String(d)))
  child.unref()
  writeFileSync(join(__dirname, '.auth', 'server.json'), JSON.stringify({ pid: child.pid, data, log }))
  const deadline = Date.now() + 30_000
  for (;;) {
    try {
      const r = await fetch(`${BASE_URL}/healthz`)
      if (r.ok) break
    } catch {
      /* not yet */
    }
    if (Date.now() > deadline) throw new Error(`ore serve did not start:\n${chunks.join('')}`)
    await new Promise((r) => setTimeout(r, 200))
  }
  writeFileSync(log, chunks.join(''))

  // Seed: org, repo at L3 (lands when green), files, a red run, issues.
  await api('POST', '/api/v0/orgs', { name: 'acme' })
  await api('POST', '/api/v0/repos', { org: 'acme', name: 'app', autonomy_level: 3, deploy_port: 8080 })
  await api('POST', '/api/v0/repos/acme/app/commits', { branch: 'main', message: 'Initial app with a failing test', files: Object.entries(DEMO_FILES).map(([path, content]) => ({ path, content })) })
  await api('POST', '/api/v0/repos/acme/app/runs', { wait: true })
  await api('POST', '/api/v0/repos/acme/app/issues', { title: 'Greeting should say Hello, Ore!', body: 'TestGreeting fails: `main.go` still returns the old greeting.\n\n```\n--- FAIL: TestGreeting\n```' })
  await api('POST', '/api/v0/repos/acme/app/issues', { title: 'Retry loop never backs off', body: 'Add exponential backoff.\n\n<script>alert("issue xss")</script><img src=x onerror="alert(1)"> This sentence must still render.' })
  // A branch with a rename and a long-named file, opened as a change.
  await api('POST', '/api/v0/repos/acme/app/commits', { branch: 'feature/guide-polish', message: 'docs: polish the guide', files: [{ path: 'docs/guide.md', content: DEMO_FILES['docs/guide.md'] + '\n## Rollback\n\nUse `ore deploy rollback`.\n' }] })
  await api('POST', '/api/v0/repos/acme/app/commits', { branch: 'feature/guide-polish', message: 'docs: move old-name', files: [{ path: 'docs/old-name.md', delete: true }, { path: 'docs/moved/new-name.md', content: DEMO_FILES['docs/old-name.md'] }] })
  await api('POST', '/api/v0/repos/acme/app/changes', { source_ref: 'refs/heads/feature/guide-polish', title: 'Polish the guide', body: 'Expands the guide and moves a file.' })
  // A big change: 50 files × 400 changed lines, for the render budget.
  const big: Array<{ path: string; content: string }> = []
  for (let f = 0; f < 50; f++) {
    const lines: string[] = []
    for (let l = 0; l < 400; l++) lines.push(`export const value${f}_${l} = compute(${l}) // generated`)
    big.push({ path: `gen/module${f}.ts`, content: lines.join('\n') + '\n' })
  }
  await api('POST', '/api/v0/repos/acme/app/commits', { branch: 'feature/big', message: 'generated: 20,000 lines', files: big })
  await api('POST', '/api/v0/repos/acme/app/changes', { source_ref: 'refs/heads/feature/big', title: 'A 20,000-line change', body: 'Synthetic.' })
  // A branch whose pipeline prints 50,000 lines.
  await api('POST', '/api/v0/repos/acme/app/commits', { branch: 'feature/noisy', message: 'noisy pipeline', files: [{ path: '.ore/pipeline.yaml', content: 'version: 0\ntasks:\n  - id: noise\n    run: \'seq 1 50000; echo "FAIL synthetic failure line 50001"; exit 1\'\n' }] })

  // Sign in once and save the cookie for every test.
  mkdirSync(join(__dirname, '.auth'), { recursive: true })
  const browser = await chromium.launch()
  const ctx = await browser.newContext({ baseURL: BASE_URL })
  const page = await ctx.newPage()
  await page.goto('/app/login')
  await page.getByLabel('Access token').fill(TOKEN)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await page.waitForURL(/\/app\/?$/)
  await ctx.storageState({ path: join(__dirname, '.auth', 'state.json') })
  await browser.close()
}
