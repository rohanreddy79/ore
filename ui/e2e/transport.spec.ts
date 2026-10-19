import { expect, test } from '@playwright/test'
import { api } from './api'

// An organization with the git transport off (ADR 0016 §3): the repository
// page teaches the Ore client, never git clone or git push. Its own
// organization, so the rest of the suite keeps git on.
test('git transport off: the clone dialog and the empty repository teach the Ore client', async ({ page }) => {
  await api('POST', '/api/v0/orgs', { name: 'nogit' })
  await api('POST', '/api/v0/repos', { org: 'nogit', name: 'svc' })
  await api('PATCH', '/api/v0/orgs/nogit/settings', { git_transport: 'off', reason: 'last day of git was Friday' })

  await page.goto('/app/nogit/svc')
  const empty = page.getByRole('status').filter({ hasText: 'This repository is empty' })
  const command = empty.locator('.copybox')
  await expect(command).toContainText('ore commit nogit/svc -m "import"')
  await expect(command).not.toContainText('git push')
  await expect(command).not.toContainText('git remote')
  await expect(empty).toContainText('Reason: last day of git was Friday')

  await page.getByRole('button', { name: 'Open' }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByRole('heading', { name: 'Open this repository' })).toBeVisible()
  await expect(dialog).toContainText('Git is off for nogit')
  await expect(dialog).toContainText('Reason: last day of git was Friday')
  await expect(dialog).toContainText('ore open nogit/svc')
  await expect(dialog).not.toContainText('git clone')

  // Read-only: the clone commands come back, with the ore save route for pushes.
  await api('PATCH', '/api/v0/orgs/nogit/settings', { git_transport: 'read-only', reason: 'pushes go through ore save' })
  await page.reload()
  await page.getByRole('button', { name: 'Clone' }).click()
  await expect(page.getByRole('dialog')).toContainText('Git pushes are off for nogit')
  await expect(page.getByRole('dialog')).toContainText('git clone')
  await expect(page.getByRole('dialog')).toContainText('ore save')
})
