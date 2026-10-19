import { expect, test } from '@playwright/test'
import { unfocus } from './api'

test.describe('keyboard-only navigation', () => {
  test('palette, go-to chords, list navigation and help', async ({ page }) => {
    await page.goto('/app/')
    // The shortcut listener mounts with the shell; wait for the page before pressing keys.
    await page.locator('main h1').first().waitFor()
    await unfocus(page)
    // ⌘K / Ctrl+K opens the palette; typing filters; Enter navigates.
    await page.keyboard.press(process.platform === 'darwin' ? 'Meta+k' : 'Control+k')
    const box = page.getByRole('combobox', { name: 'Search or jump to' })
    await expect(box).toBeFocused()
    await box.fill('acme/app')
    await page.keyboard.press('Enter')
    await expect(page).toHaveURL(/\/app\/acme\/app$/)
    // g i → issues, g r → changes, g p → pipelines, g l → ledger, g c → code
    for (const [keys, path] of [
      ['g i', /\/issues$/],
      ['g r', /\/changes$/],
      ['g p', /\/pipelines$/],
      ['g d', /\/deployments$/],
      ['g a', /\/agents$/],
      ['g l', /\/ledger$/],
      ['g c', /\/acme\/app$/],
    ] as const) {
      await page.locator('main h1').first().waitFor()
      await unfocus(page)
      for (const k of keys.split(' ')) await page.keyboard.press(k)
      await expect(page).toHaveURL(path)
    }
    // ? opens the shortcut help; Escape closes it.
    await page.keyboard.press('Shift+?')
    await expect(page.getByRole('dialog', { name: 'Keyboard shortcuts' })).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(page.getByRole('dialog', { name: 'Keyboard shortcuts' })).toBeHidden()
    // Tab reaches the top bar controls and the tabs, with visible focus.
    await page.goto('/app/acme/app/issues')
    await page.keyboard.press('Tab')
    const first = page.locator(':focus')
    await expect(first).toBeVisible()
    for (let i = 0; i < 12; i++) await page.keyboard.press('Tab')
    const focused = await page.evaluate(() => {
      const el = document.activeElement as HTMLElement
      const cs = getComputedStyle(el)
      return { tag: el.tagName, text: el.textContent?.trim().slice(0, 30), ring: cs.boxShadow !== 'none' || cs.outlineStyle !== 'none' }
    })
    expect(focused.ring).toBe(true)
    // / opens search; c opens the new-issue form from the list.
    await page.locator('main h1').first().waitFor()
    await unfocus(page)
    await page.keyboard.press('/')
    await expect(page.getByRole('combobox', { name: 'Search or jump to' })).toBeFocused()
    await page.keyboard.press('Escape')
    await unfocus(page)
    await page.keyboard.press('c')
    await expect(page).toHaveURL(/\/issues\/new$/)
    await expect(page.getByLabel('Title')).toBeFocused()
  })
})
