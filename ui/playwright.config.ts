import { defineConfig, devices } from '@playwright/test'

// The suite runs against a real `ore serve` (host executor, scripted agent)
// started by e2e/global-setup.ts on this port and seeded through the API.
export const PORT = Number(process.env.ORE_E2E_PORT ?? 7799)
export const BASE_URL = `http://127.0.0.1:${PORT}`
export const TOKEN = process.env.ORE_E2E_TOKEN ?? 'ore-e2e-token'

export default defineConfig({
  testDir: './e2e',
  globalSetup: './e2e/global-setup.ts',
  globalTeardown: './e2e/global-teardown.ts',
  timeout: 90_000,
  expect: { timeout: 15_000 },
  fullyParallel: false,
  workers: 1,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : [['list']],
  outputDir: './test-results',
  use: {
    baseURL: BASE_URL,
    storageState: './e2e/.auth/state.json',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    viewport: { width: 1280, height: 800 },
    colorScheme: 'light',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
})
