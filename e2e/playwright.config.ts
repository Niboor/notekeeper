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
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
    // The other evergreen engines run the tests tagged @cross (WEB-N6). They need their browsers and system
    // libraries (`npx playwright install --with-deps firefox webkit`), so they run when E2E_BROWSERS=all, as in CI.
    ...(process.env.E2E_BROWSERS === 'all'
      ? [
          { name: 'firefox', grep: /@cross/, use: { ...devices['Desktop Firefox'] } },
          { name: 'webkit', grep: /@cross/, use: { ...devices['Desktop Safari'] } },
        ]
      : []),
  ],
})
