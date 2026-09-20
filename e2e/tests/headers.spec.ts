import { expect, test } from '@playwright/test'

// SEC-API-8, SEC-SHR-5, SEC-SHR-6, SEC-OPS-4, CORE-SH8, CORE-SH10: what the web image's nginx sends and routes.
// Only meaningful behind that nginx, so it runs with `make test-e2e-stack` and is skipped elsewhere.
test.skip(!process.env.E2E_STACK, 'needs the built web image (make test-e2e-stack)')

test('application pages carry a strict CSP and the baseline security headers', async ({ request }) => {
  for (const path of ['/', '/settings', '/p/anything']) {
    const res = await request.get(path)
    expect(res.status(), path).toBe(200)
    const h = res.headers()
    const csp = h['content-security-policy'] ?? ''
    expect(csp).toContain("script-src 'self'")
    expect(csp).toContain("frame-ancestors 'none'")
    expect(csp).toContain("object-src 'none'")
    expect(csp.replace("style-src-attr 'unsafe-inline'", '')).not.toContain('unsafe-inline') // no inline scripts, no inline stylesheets
    expect(csp).not.toContain('unsafe-eval')
    expect(h['strict-transport-security']).toContain('max-age=')
    expect(h['x-content-type-options']).toBe('nosniff')
    expect(h['referrer-policy']).toBe('no-referrer')
    expect(h['x-frame-options']).toBe('DENY')
    expect(h['cache-control']).toBe('no-cache') // the shell is revalidated
  }
  const sw = await request.get('/sw.js')
  expect(sw.headers()['cache-control']).toBe('no-cache')
  // The API answers keep their own headers, and the metrics, ops and bot endpoints are not reachable at all.
  expect((await request.get('/api/v1/version')).headers()['cache-control']).toBe('no-store')
  // Anything that is not the app answers with the app's own page (client-side routing) or a 404, never with Core's ops, bot or public API.
  for (const path of ['/metrics', '/healthz', '/readyz', '/bot/v1/version', '/api/public/v1/version', '/api/other']) {
    const res = await request.get(path)
    const type = res.headers()['content-type'] ?? ''
    const body = await res.text()
    expect(res.status() === 404 || (type.includes('text/html') && body.includes('<div id="root">')), path).toBe(true)
    expect(body, path).not.toContain('nk_http_requests_total')
    expect(body, path).not.toMatch(/^ok\s*$/)
  }
})

test('the share host serves only the share page and its API, without cookies, indexing or referrers', async ({ playwright }) => {
  const share = await playwright.request.newContext({ baseURL: `http://share.localhost:${new URL(process.env.E2E_BASE_URL ?? 'http://localhost:8088').port}` })
  const page = await share.get('/s')
  expect(page.status()).toBe(200)
  const h = page.headers()
  expect(h['x-robots-tag']).toContain('noindex')
  expect(h['referrer-policy']).toBe('no-referrer')
  expect(h['cache-control']).toBe('no-store')
  expect(h['content-security-policy']).toContain("script-src 'self'")
  expect(h['set-cookie']).toBeUndefined()
  // Nothing else of the app exists on this host: not its pages, not its API, not its worker.
  for (const path of ['/', '/settings', '/login', '/api/v1/me', '/api/v1/version', '/sw.js', '/metrics', '/admin']) {
    expect((await share.get(path)).status(), path).toBe(404)
  }
  // The public API answers the same generic 404 for anything but a live link, and never sets a cookie.
  const api = await share.get('/api/public/v1/share', { headers: { 'X-Share-Token': 'A'.repeat(32), Cookie: '__Host-nka=stolen' } })
  expect(api.status()).toBe(404)
  expect(api.headers()['set-cookie']).toBeUndefined()
  expect(api.headers()['x-robots-tag']).toContain('noindex')
  await share.dispose()
})
