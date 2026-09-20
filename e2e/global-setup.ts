import { chromium, type FullConfig } from '@playwright/test'
import { PASSWORD, SENDER } from './tests/helpers'

/**
 * Activates the test user from the operator's activation link, links a chat identity to the bot
 * (so chat messages can be simulated through the bot API), and stores the signed-in session for
 * the tests. Everything after this uses that session.
 */
export default async function globalSetup(config: FullConfig): Promise<void> {
  const baseURL = config.projects[0]!.use.baseURL!
  const browser = await chromium.launch()
  const context = await browser.newContext({ baseURL })
  const page = await context.newPage()

  await page.goto(`/activate#${process.env.E2E_ACTIVATION_TOKEN}`)
  await page.getByLabel('New password').fill(PASSWORD)
  await page.getByRole('button', { name: /Set password/ }).click()
  await page.getByRole('button', { name: 'New note' }).waitFor()

  const call = (method: string, path: string, body?: unknown) =>
    page.evaluate(
      async ({ method, path, body }) => {
        const res = await fetch(path, {
          method, headers: { 'Content-Type': 'application/json', 'X-Notekeeper-Client': 'web' },
          body: body === undefined ? undefined : JSON.stringify(body),
        })
        return res.json() as Promise<{ items?: { id: string }[]; code?: string }>
      },
      { method, path, body },
    )
  const bots = await call('GET', '/api/v1/bot-instances')
  const pairing = await call('POST', '/api/v1/me/pairing-codes', { bot_instance_id: bots.items![0]!.id })
  const res = await fetch(`${process.env.E2E_BOT_URL}/bot/v1/commands`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${process.env.E2E_BOT_KEY}`, 'Content-Type': 'application/json' },
    body: JSON.stringify({ command: 'link', args: pairing.code, sender: SENDER, conversation: '!e2e:localhost' }),
  })
  const linked = (await res.json()) as { ok: boolean }
  if (!linked.ok) throw new Error('linking the chat identity failed')

  await context.storageState({ path: 'auth.json' })
  await browser.close()
}
