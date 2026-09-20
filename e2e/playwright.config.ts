import { defineConfig, devices } from '@playwright/test'

// The stack (Core, PostgreSQL, the built web app) is started by run.sh, which passes its
// addresses and setup secrets through E2E_* variables.
export default defineConfig({
  testDir: './tests',
  fullyParallel: false,
  workers: 1, // one shared database: the tests build on each other's users but use their own data
  retries: 0,
  reporter: [['list']],
  timeout: 60_000,
  globalSetup: './global-setup.ts',
  expect: { timeout: 10_000 },
  use: {
    baseURL: process.env.E2E_BASE_URL ?? 'http://localhost:4174',
    storageState: 'auth.json',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    // Cookies are Secure; Chromium accepts them on http://localhost.
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
})
