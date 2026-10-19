import { expect, test } from '@playwright/test'
import { settle } from './api'

// Lightweight accessibility checks without an extra dependency: every button
// and input has an accessible name, images have alt text, and the contrast of
// body text against its background meets AA in both themes.
async function audit(page: import('@playwright/test').Page) {
  return page.evaluate(() => {
    const problems: string[] = []
    const name = (el: Element) => (el.getAttribute('aria-label') || el.getAttribute('aria-labelledby') || el.getAttribute('title') || (el as HTMLElement).innerText || '').trim()
    for (const b of document.querySelectorAll('button, [role=button], a[href]')) if (!name(b)) problems.push(`unnamed control: ${b.outerHTML.slice(0, 80)}`)
    for (const i of document.querySelectorAll('input, select, textarea')) {
      const id = i.getAttribute('id')
      const labelled = i.getAttribute('aria-label') || i.getAttribute('aria-labelledby') || (id && document.querySelector(`label[for="${id}"]`)) || i.closest('label')
      if (!labelled && i.getAttribute('type') !== 'hidden') problems.push(`unlabelled input: ${i.outerHTML.slice(0, 80)}`)
    }
    for (const img of document.querySelectorAll('img')) if (!img.hasAttribute('alt')) problems.push('img without alt')
    const lum = (c: string) => {
      const m = c.match(/\d+(\.\d+)?/g)!.map(Number)
      const f = (v: number) => {
        v /= 255
        return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4
      }
      return 0.2126 * f(m[0]!) + 0.7152 * f(m[1]!) + 0.0722 * f(m[2]!)
    }
    const body = getComputedStyle(document.body)
    const bg = lum(body.backgroundColor)
    const fg = lum(body.color)
    const ratio = (Math.max(bg, fg) + 0.05) / (Math.min(bg, fg) + 0.05)
    if (ratio < 4.5) problems.push(`body contrast ${ratio.toFixed(2)}`)
    const muted = document.querySelector('.muted, .sub')
    if (muted) {
      const m = lum(getComputedStyle(muted).color)
      const r2 = (Math.max(bg, m) + 0.05) / (Math.min(bg, m) + 0.05)
      if (r2 < 4.5) problems.push(`muted contrast ${r2.toFixed(2)}`)
    }
    return problems
  })
}

for (const scheme of ['light', 'dark'] as const) {
  test(`controls are labelled and contrast meets AA (${scheme})`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: scheme })
    for (const path of ['/app/', '/app/acme/app', '/app/acme/app/issues', '/app/acme/app/changes/1', '/app/acme/app/pipelines', '/app/acme/app/deployments', '/app/acme/app/agents', '/app/acme/app/ledger', '/app/acme/app/settings']) {
      await page.goto(path)
      await settle(page)
      const problems = await audit(page)
      expect(problems, path).toEqual([])
    }
  })
}
