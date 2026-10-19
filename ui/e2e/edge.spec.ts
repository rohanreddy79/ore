import { expect, test } from '@playwright/test'
import { api } from './api'

test.describe('edge cases', () => {
  test('repository, change and issue that do not exist show a friendly 404', async ({ page }) => {
    await page.goto('/app/acme/nope')
    await expect(page.getByText('This repository does not exist')).toBeVisible()
    await expect(page.getByRole('link', { name: 'Go home' })).toBeVisible()
    await page.goto('/app/acme/app/changes/999')
    await expect(page.getByText('This change does not exist')).toBeVisible()
    await page.goto('/app/acme/app/issues/999')
    await expect(page.getByText('This issue does not exist')).toBeVisible()
    await page.goto('/app/acme/app/blob/main/does/not/exist.txt')
    await expect(page.getByText('This file does not exist')).toBeVisible()
  })

  test('an API 5xx shows a toast with retry and never a blank screen', async ({ page }) => {
    let fail = true
    await page.route('**/api/v0/repos/acme/app/issues', (route) => (fail ? route.fulfill({ status: 500, contentType: 'application/json', body: '{"error":"ledger exploded"}' }) : route.continue()))
    await page.goto('/app/acme/app/issues')
    await expect(page.getByRole('alert').filter({ hasText: 'Server error (500)' })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Issues' })).toBeVisible()
    await expect(page.getByText('Something went wrong')).toBeVisible()
    fail = false
    await page.getByRole('button', { name: 'Retry' }).first().click()
    await expect(page.locator('.list-row').first()).toBeVisible()
  })

  test('the ledger stream reconnects after a disconnect without duplicating events', async ({ page }) => {
    let dropped = false
    await page.route('**/api/v0/webapp/events/stream**', async (route) => {
      if (!dropped) {
        dropped = true
        // Abort the first connection a moment after it opens: the client must reconnect.
        return route.abort('connectionreset')
      }
      return route.continue()
    })
    await page.goto('/app/acme/app/ledger')
    await expect(page.locator('.live-dot.open')).toBeVisible({ timeout: 20_000 })
    const title = `dup-check-${Date.now()}`
    await api('POST', '/api/v0/repos/acme/app/issues', { title })
    await expect(page.locator('.event-row', { hasText: title })).toHaveCount(1, { timeout: 20_000 })
    await page.waitForTimeout(2500)
    await expect(page.locator('.event-row', { hasText: title })).toHaveCount(1)
    expect(dropped).toBe(true)
  })

  test('a Markdown comment with script tags renders inert', async ({ page }) => {
    let dialogs = 0
    page.on('dialog', (d) => {
      dialogs++
      void d.dismiss()
    })
    await page.goto('/app/acme/app/issues/2')
    await expect(page.locator('.comment-body')).toContainText('This sentence must still render')
    expect(await page.locator('.comment-body script').count()).toBe(0)
    expect(await page.locator('.comment-body img[onerror]').count()).toBe(0)
    expect(dialogs).toBe(0)
  })

  test('a diff with a renamed file, and a very long file name in the tree', async ({ page }) => {
    const changes = (await api<Array<{ number: number; title: string }> | null>('GET', '/api/v0/repos/acme/app/changes')) ?? []
    const ch = changes.find((c) => c.title === 'Polish the guide')!
    await page.goto(`/app/acme/app/changes/${ch.number}?tab=files`)
    await expect(page.locator('.file-header', { hasText: 'new-name.md' })).toContainText('→')
    await expect(page.locator('.file-header', { hasText: 'new-name.md' })).toContainText('renamed')
    await page.goto('/app/acme/app/tree/main/docs')
    const long = page.locator('.list-row', { hasText: 'a-very-long-file-name' })
    await expect(long).toBeVisible()
    const box = await long.boundingBox()
    expect(box!.width).toBeLessThanOrEqual(1300)
    await long.click()
    await expect(page.locator('.breadcrumb-path .leaf')).toContainText('a-very-long-file-name')
  })

  test('a change with zero patch sets explains itself', async ({ page }) => {
    const changes = (await api<Array<{ number: number; title: string }> | null>('GET', '/api/v0/repos/acme/app/changes')) ?? []
    const ch = changes.find((c) => c.title === 'Polish the guide')!
    await page.route(`**/api/v0/repos/acme/app/changes/${ch.number}/patchsets`, (r) => r.fulfill({ status: 200, contentType: 'application/json', body: '[]' }))
    await page.route(`**/api/v0/repos/acme/app/changes/${ch.number}/diff`, (r) => r.fulfill({ status: 200, contentType: 'text/x-diff', body: '' }))
    await page.goto(`/app/acme/app/changes/${ch.number}`)
    await expect(page.getByText('None recorded.')).toBeVisible()
    await page.goto(`/app/acme/app/changes/${ch.number}?tab=files`)
    await expect(page.getByText('no patch sets yet')).toBeVisible()
  })

  test('absent fleet, learn and webhooks APIs hide their sections with a hint and no errors', async ({ page }) => {
    // Simulate a server without those extensions, whatever this server has.
    const absent = ['agent.fleet', 'learn', 'connect.webhooks']
    await page.route('**/api/v0/webapp/features', async (r) => {
      const resp = await r.fetch()
      const json = await resp.json()
      json.fleet = false
      json.learn = false
      json.webhooks = false
      json.extensions = (json.extensions || []).filter((n: string) => !absent.includes(n))
      await r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(json) })
    })
    const gone = (r: { fulfill: (o: object) => Promise<void> }) =>
      r.fulfill({ status: 404, contentType: 'application/json', body: '{"error":"not found"}' })
    await page.route('**/api/v0/fleet/**', gone)
    await page.route('**/api/v0/repos/*/*/fleet/**', gone)
    await page.route('**/api/v0/learn/**', gone)
    await page.route('**/api/v0/repos/*/*/learn/**', gone)
    await page.route('**/api/v0/webhooks/**', gone)
    const errors: string[] = []
    page.on('pageerror', (e) => errors.push(e.message))
    page.on('console', (m) => {
      if (m.type() === 'error') errors.push(m.text())
    })
    await page.goto('/app/acme/app/agents')
    await expect(page.getByText('arrive with the agent.fleet extension')).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Evidence', exact: true })).toHaveCount(0)
    await page.goto('/app/acme/app/settings/webhooks')
    await expect(page.getByText('connect.webhooks')).toBeVisible()
    await page.goto('/app/')
    await expect(page.getByRole('heading', { name: 'Repositories' })).toBeVisible()
    expect(errors.filter((e) => !/favicon/.test(e))).toEqual([])
  })

  test('works at 375 px width', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 740 })
    await page.goto('/app/acme/app/issues')
    await expect(page.getByRole('heading', { name: 'Issues' })).toBeVisible()
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
    expect(overflow).toBeLessThanOrEqual(1)
    await page.goto('/app/acme/app/changes/1?tab=files')
    await expect(page.locator('.diff-summary')).toBeVisible()
    const overflow2 = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
    expect(overflow2).toBeLessThanOrEqual(1)
  })
})
