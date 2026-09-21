import { expect, test } from '@playwright/test'
import { api, botCommand, botSend, card, createNote, lane, openFreshBoard } from './helpers'

// F9 / WEB-18 / CORE-R1, R3, R5, R9: a reminder set from a chat message shows on the note, fires on time
// without anyone running anything, and reaches the person through the bell, which leads back to the note.
test('a reminder set in chat fires and shows in the bell', async ({ page }) => {
  const fx = await openFreshBoard(page)
  const messageId = await botSend('pick up the parcel from the post office')
  await expect(card(lane(page, 'Inbox'), 'pick up the parcel')).toBeVisible({ timeout: 10_000 })

  // `!remind` as a reply puts a reminder on that note, and the card shows it at once.
  const res = await botCommand('remind', 'tomorrow 9am', messageId)
  expect(res.ok).toBe(true)
  expect(res.reply).toContain('remind you')
  const parcel = card(lane(page, 'Inbox'), 'pick up the parcel')
  await expect(parcel.locator('.reminder-chip')).toBeVisible({ timeout: 10_000 })
  // Unknown times are refused with an example, and nothing is created.
  const bad = await botCommand('remind', 'whenever you like', messageId)
  expect(bad.ok).toBe(false)
  expect(bad.reply).toContain('Try')

  // A reminder due in a few seconds fires by itself; the bell shows it.
  const noteId = await createNote(page, 'water the plants', fx.columns.Todo)
  await page.reload()
  const due = new Date(Date.now() + 4000).toISOString()
  expect((await api(page, 'POST', `/api/v1/notes/${noteId}/reminders`, { due_at: due })).status).toBe(201)
  const bell = page.getByRole('button', { name: /^Notifications/ })
  await expect(bell).toHaveAccessibleName(/unread/, { timeout: 30_000 })
  await bell.click()
  await page.getByRole('link', { name: /Reminder: water the plants/ }).click()
  await expect(page).toHaveURL(new RegExp(`/p/${fx.pageId}\\?note=${noteId}`))
  await expect(card(lane(page, 'Todo'), 'water the plants')).toBeFocused()
  await expect(card(lane(page, 'Todo'), 'water the plants').locator('.reminder-chip')).toContainText('Reminded')

  // Snooze from the dialog: the reminder is armed again.
  await card(lane(page, 'Todo'), 'water the plants').locator('.reminder-chip').click()
  const dialog = page.getByRole('dialog', { name: 'Reminders' })
  await dialog.getByRole('button', { name: 'Snooze 1 hour' }).click()
  await expect(dialog.getByText(/^Due /)).toBeVisible()
  await dialog.getByRole('button', { name: 'Close' }).click()

  // The upcoming list shows what is armed.
  await page.goto('/reminders')
  await expect(page.getByRole('link', { name: 'water the plants' })).toBeVisible()
  await expect(page.getByRole('link', { name: /pick up the parcel/ })).toBeVisible()
})

// WEB-18 / CORE-R2: a custom reminder gets its date and its time (hour and minute) from separate fields, which
// every browser shows the same way, and is due at exactly that local time.
test('pick the date, hour and minute of a reminder @cross', async ({ page }) => {
  const fx = await openFreshBoard(page)
  const noteId = await createNote(page, 'renew the passport', fx.columns.Todo)
  await page.reload()
  const note = card(lane(page, 'Todo'), 'renew the passport')
  await note.hover()
  await note.getByRole('button', { name: 'More actions' }).click()
  await page.getByRole('menuitem', { name: 'Remind me…' }).click()
  const dialog = page.getByRole('dialog', { name: 'Reminders' })

  const target = new Date()
  target.setDate(target.getDate() + 3)
  const day = `${target.getFullYear()}-${String(target.getMonth() + 1).padStart(2, '0')}-${String(target.getDate()).padStart(2, '0')}`
  await dialog.getByLabel('Date').fill(day)
  await dialog.getByLabel('Hour').selectOption('17')
  await dialog.getByLabel('Minute').selectOption('45')
  await dialog.getByRole('button', { name: 'Set reminder' }).click()
  await expect(dialog.getByText(/^Due /)).toBeVisible()

  const expected = await page.evaluate((d) => {
    const [y, m, dd] = d.split('-').map(Number)
    return new Date(y!, m! - 1, dd!, 17, 45).toISOString()
  }, day)
  const upcoming = await api<{ items: { note_id: string; due_at: string }[] }>(page, 'GET', '/api/v1/reminders')
  const mine = upcoming.body.items.find((r) => r.note_id === noteId)
  expect(mine && new Date(mine.due_at).toISOString()).toBe(expected)
})
