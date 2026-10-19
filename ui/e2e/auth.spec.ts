import { expect, test } from '@playwright/test'
import { TOKEN } from '../playwright.config'

test.describe('authentication', () => {
  test.use({ storageState: { cookies: [], origins: [] } })

  test('an unauthenticated deep link redirects to sign-in and returns afterwards', async ({ page }) => {
    await page.goto('/app/acme/app/issues/2')
    await expect(page).toHaveURL(/\/app\/login\?next=/)
    await page.getByLabel('Access token').fill('not-a-token')
    await page.getByRole('button', { name: 'Sign in' }).click()
    await expect(page.getByRole('alert')).toContainText('not valid')
    await page.getByLabel('Access token').fill(TOKEN)
    await page.getByRole('button', { name: 'Sign in' }).click()
    await expect(page).toHaveURL(/\/app\/acme\/app\/issues\/2$/)
    await expect(page.locator('.page-head h1')).toContainText('Retry loop never backs off')
  })

  test('/login?token= (what ore serve prints) signs in and strips the token from the address', async ({ page }) => {
    await page.goto(`/app/login?token=${TOKEN}`)
    await expect(page).toHaveURL(/\/app\/?$/)
    expect(page.url()).not.toContain(TOKEN)
    await expect(page.getByRole('heading', { name: 'Repositories' })).toBeVisible()
  })

  test('signing out clears the session', async ({ page }) => {
    await page.goto(`/app/login?token=${TOKEN}`)
    await expect(page.getByRole('heading', { name: 'Repositories' })).toBeVisible()
    await page.getByRole('button', { name: 'Account menu' }).click()
    await page.getByRole('menuitem', { name: 'Sign out' }).click()
    await expect(page).toHaveURL(/\/app\/login/)
    await page.goto('/app/')
    await expect(page).toHaveURL(/\/app\/login/)
  })
})
