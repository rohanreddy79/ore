import { mkdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = dirname(fileURLToPath(import.meta.url))
import { expect, test } from '@playwright/test'
import { api, settle } from './api'

const dir = join(__dirname, 'screenshots')
mkdirSync(dir, { recursive: true })

test.describe('screenshots in both themes', () => {
  for (const scheme of ['light', 'dark'] as const) {
    test(`${scheme}`, async ({ page }) => {
      test.setTimeout(180_000)
      await page.emulateMedia({ colorScheme: scheme })
      const changes = (await api<Array<{ number: number; title: string }> | null>('GET', '/api/v0/repos/acme/app/changes')) ?? []
      const polish = changes.find((c) => c.title === 'Polish the guide') ?? { number: 1 }
      const runs = (await api<Array<{ id: string }> | null>('GET', '/api/v0/repos/acme/app/runs')) ?? []
      const agents = (await api<Array<{ id: string }> | null>('GET', '/api/v0/repos/acme/app/agent/runs')) ?? []
      const shots: Array<[string, string]> = [
        ['home', '/app/'],
        ['code', '/app/acme/app'],
        ['file', '/app/acme/app/blob/main/main.go'],
        ['issues', '/app/acme/app/issues?state=all'],
        ['issue', '/app/acme/app/issues/2'],
        ['changes', '/app/acme/app/changes?state=all'],
        ['change-conversation', `/app/acme/app/changes/${polish.number}`],
        ['change-files', `/app/acme/app/changes/${polish.number}?tab=files`],
        ['pipelines', '/app/acme/app/pipelines'],
        ['run', `/app/acme/app/pipelines/${runs[0]?.id ?? ''}`],
        ['deployments', '/app/acme/app/deployments'],
        ['agents', '/app/acme/app/agents'],
        ['agent-run', agents[0] ? `/app/acme/app/agents/${agents[0].id}` : '/app/acme/app/agents'],
        ['ledger', '/app/acme/app/ledger'],
        ['settings', '/app/acme/app/settings'],
        ['design', '/app/design'],
      ]
      for (const [name, path] of shots) {
        await page.goto(path)
        await settle(page)
        await page.waitForTimeout(300)
        await page.screenshot({ path: join(dir, `${name}-${scheme}.png`), fullPage: name !== 'change-files' && name !== 'run' })
      }
      await page.setViewportSize({ width: 375, height: 812 })
      await page.goto('/app/acme/app/issues')
      await settle(page)
      await page.screenshot({ path: join(dir, `mobile-issues-${scheme}.png`), fullPage: true })
      await page.goto(`/app/acme/app/changes/${polish.number}?tab=files`)
      await settle(page)
      await page.screenshot({ path: join(dir, `mobile-change-${scheme}.png`) })
      expect(true).toBe(true)
    })
  }
})
