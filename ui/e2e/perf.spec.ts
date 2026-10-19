import { expect, test } from '@playwright/test'
import { api, settle } from './api'

test('a 20,000-line change renders interactively under 200 ms after data arrives', async ({ page }) => {
  const changes = (await api<Array<{ number: number; title: string }> | null>('GET', '/api/v0/repos/acme/app/changes')) ?? []
  const ch = changes.find((c) => c.title === 'A 20,000-line change')!
  await page.goto(`/app/acme/app/changes/${ch.number}?tab=files`)
  await expect(page.locator('.diff-summary')).toContainText('50 files changed')
  await expect(page.locator('.diff-summary')).toContainText('+20000')
  const measure = await page.evaluate(() => {
    const m = performance.getEntriesByName('ore:diff', 'measure').at(-1)
    return m ? m.duration : null
  })
  expect(measure).not.toBeNull()
  console.log(`ore:diff render after data: ${measure!.toFixed(1)} ms`)
  expect(measure!).toBeLessThan(200)
  // Files over 400 lines are collapsed; loading one keeps the page responsive.
  await expect(page.locator('.collapsed-box').first()).toContainText('collapsed by default')
  const t0 = Date.now()
  await page.getByRole('button', { name: 'Load diff' }).first().click()
  await expect(page.locator('.line-grid').first()).toBeVisible()
  expect(Date.now() - t0).toBeLessThan(2000)
  // Only a window of rows is in the DOM.
  const rows = await page.locator('.diff-row').count()
  expect(rows).toBeLessThan(150)
  // No layout shift on the main pages: the CLS score stays near zero.
  for (const path of ['/app/', '/app/acme/app', '/app/acme/app/issues', '/app/acme/app/changes']) {
    await page.goto(path)
    await settle(page)
    const cls = await page.evaluate(
      () =>
        new Promise<number>((resolve) => {
          let total = 0
          const obs = new PerformanceObserver((list) => {
            for (const e of list.getEntries() as Array<PerformanceEntry & { value: number; hadRecentInput: boolean }>) if (!e.hadRecentInput) total += e.value
          })
          obs.observe({ type: 'layout-shift', buffered: true })
          setTimeout(() => {
            obs.disconnect()
            resolve(total)
          }, 800)
        }),
    )
    expect(cls, `CLS on ${path}`).toBeLessThan(0.1)
  }
})
