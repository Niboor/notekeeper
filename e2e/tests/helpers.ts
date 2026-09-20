import { expect, type Locator, type Page } from '@playwright/test'

export const PASSWORD = 'correct horse battery staple'
export const USER = 'alice'
export const SENDER = '@alice:localhost'

/** Calls the user API from inside the page, so the browser's own cookies and CSRF header apply. */
export async function api<T = unknown>(page: Page, method: string, path: string, body?: unknown): Promise<{ status: number; body: T }> {
  return page.evaluate(
    async ({ method, path, body }) => {
      const res = await fetch(path, {
        method,
        headers: { 'Content-Type': 'application/json', 'X-Notekeeper-Client': 'web' },
        body: body === undefined ? undefined : JSON.stringify(body),
      })
      const text = await res.text()
      return { status: res.status, body: text ? JSON.parse(text) : null }
    },
    { method, path, body },
  )
}

/** Sends a chat message to the bot API as the linked user, as the Matrix bot would. */
export async function botSend(text: string): Promise<void> {
  const id = `$e2e-${Date.now()}-${Math.random().toString(36).slice(2)}`
  const res = await fetch(`${process.env.E2E_BOT_URL}/bot/v1/events`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${process.env.E2E_BOT_KEY}`, 'Content-Type': 'application/json' },
    body: JSON.stringify({
      event_id: id, kind: 'message_created', sender: SENDER, conversation: '!e2e:localhost', message_id: id,
      timestamp: new Date(Date.now() + 1000).toISOString(), parts: [{ type: 'text', text }],
    }),
  })
  expect(res.status).toBe(200)
}

export interface Fixture {
  pageId: string
  pageName: string
  columns: Record<string, string> // name → id
}

/** Creates a page with columns through the API and opens it. */
export async function openFreshBoard(page: Page, columns: string[] = ['Todo', 'Done']): Promise<Fixture> {
  await page.goto('/')
  await expect(page.getByRole('button', { name: 'New note' })).toBeVisible()
  const pageName = `P${Math.random().toString(36).slice(2, 8)}`
  const created = await api<{ id: string }>(page, 'POST', '/api/v1/pages', { name: pageName })
  const cols: Record<string, string> = {}
  for (const name of columns) {
    cols[name] = (await api<{ id: string }>(page, 'POST', '/api/v1/categories', { page_id: created.body.id, name })).body.id
  }
  await page.goto(`/p/${created.body.id}`)
  // On a phone the columns are tabs and only one is on screen: either the column or its tab shows the board is ready.
  await expect(page.getByRole('region', { name: columns[0]!, exact: true }).or(page.getByRole('tab', { name: new RegExp(columns[0]!) })).first()).toBeVisible()
  return { pageId: created.body.id, pageName, columns: cols }
}

export async function createNote(page: Page, text: string, categoryId?: string): Promise<string> {
  const res = await api<{ id: string }>(page, 'POST', '/api/v1/notes', {
    category_id: categoryId ?? null, parts: [{ type: 'text', text }],
  })
  expect(res.status).toBe(201)
  return res.body.id
}

export const lane = (page: Page, name: string): Locator => page.getByRole('region', { name, exact: true })
export const card = (scope: Locator | Page, text: string): Locator => scope.locator('article.note', { hasText: text })

/** Drags a card to a point over a target with real mouse events, like a person would. */
export async function dragTo(page: Page, from: Locator, target: Locator, opts: { hold?: number } = {}): Promise<void> {
  const a = (await from.boundingBox())!
  const b = (await target.boundingBox())!
  await page.mouse.move(a.x + a.width / 2, a.y + Math.min(24, a.height / 2))
  await page.mouse.down()
  await page.mouse.move(a.x + a.width / 2 + 12, a.y + 30, { steps: 4 }) // past the 5 px activation distance
  await page.mouse.move(b.x + b.width / 2, b.y + Math.min(40, b.height / 2), { steps: 20 })
  if (opts.hold) await page.waitForTimeout(opts.hold)
  await page.mouse.up()
}
