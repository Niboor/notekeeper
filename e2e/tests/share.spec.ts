import AxeBuilder from '@axe-core/playwright'
import { expect, test } from '@playwright/test'
import { card, createNote, lane, openFreshBoard } from './helpers'

// F11 / WEB-21 / CORE-SH1..SH5, SH8, SH10: share a note from the app, open the link as a stranger on
// the cookie-free share origin, see it follow an edit, and end it.
test('share a note, open it as a stranger, and revoke the link @cross', async ({ page, browser }) => {
  const fx = await openFreshBoard(page)
  await createNote(page, 'packing list\n\n- [ ] passport\n- [x] tickets', fx.columns.Todo)
  await page.reload()

  const note = card(lane(page, 'Todo'), 'packing list')
  await note.getByRole('button', { name: 'More actions' }).click()
  await page.getByRole('menuitem', { name: 'Share…' }).click()
  const dialog = page.getByRole('dialog', { name: 'Share this note' })
  await expect(dialog.getByText('Anyone with the link can read this note')).toBeVisible()
  await dialog.getByLabel('Link works for').selectOption('1d')
  await dialog.getByRole('button', { name: 'Create link' }).click()
  const url = await dialog.getByRole('textbox').inputValue()
  expect(url).toMatch(/^https?:\/\/share\.localhost:\d+\/s#[\w-]{32}$/)
  const token = url.split('#')[1]!
  await dialog.getByRole('button', { name: 'Close' }).click()
  await expect(note.getByText('Shared', { exact: true })).toBeVisible()

  // A stranger: a fresh browser with no session, on the share origin.
  const stranger = await browser.newContext({ storageState: { cookies: [], origins: [] } })
  const shared = await stranger.newPage()
  const seen: string[] = []
  shared.on('request', (r) => seen.push(r.url()))
  await shared.goto(url)
  await expect(shared.getByText('packing list')).toBeVisible()
  await expect(shared.getByText(/shared by a Notekeeper user/)).toBeVisible()
  await expect(shared.getByRole('checkbox', { name: 'passport' })).toBeDisabled()
  expect(await stranger.cookies()).toEqual([]) // no cookie is ever set on the share origin
  expect(seen.filter((u) => u.includes(token))).toEqual([]) // the token never appears in a request address
  expect(seen.some((u) => u.includes('/api/public/v1/share'))).toBe(true)
  const serious = (await new AxeBuilder({ page: shared }).withTags(['wcag2a', 'wcag2aa']).analyze()).violations.filter((v) => v.impact === 'serious' || v.impact === 'critical')
  expect(serious.map((v) => v.id)).toEqual([])

  // An edit in the app shows on reload of the link.
  await note.getByText('passport').click({ trial: true }).catch(() => undefined)
  await note.getByRole('button', { name: 'More actions' }).click()
  await page.getByRole('menuitem', { name: 'Edit' }).click()
  await note.getByRole('textbox').fill('packing list (updated)\n\n- [ ] passport')
  await note.getByRole('button', { name: 'Save' }).click()
  await shared.reload()
  await expect(shared.getByText('packing list (updated)')).toBeVisible()

  // Settings lists the link with its usage; revoking ends it at once.
  await page.goto('/settings')
  const row = page.locator('.row', { hasText: 'packing list (updated)' })
  await expect(row).toContainText('views')
  await row.getByRole('button', { name: /Revoke/ }).click()
  await expect(page.getByText('You have no active share links.')).toBeVisible()
  await shared.reload()
  await expect(shared.getByRole('alert')).toContainText('not available')
  await stranger.close()
})
