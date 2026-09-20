import { devices, expect, test } from '@playwright/test'
import { card, createNote, lane, openFreshBoard } from './helpers'

// WEB-N1, WEB-5: on a phone the columns become tabs, notes are moved through the Move-to menu
// (dragging competes with scrolling), and a floating button adds notes.
test.use({ ...devices['Pixel 7'] })

test('a phone shows one column at a time and moves notes through the menu', async ({ page }) => {
  const fx = await openFreshBoard(page)
  await createNote(page, 'phone oat milk')
  await createNote(page, 'on the board', fx.columns.Todo)
  await page.reload()

  const tabs = page.getByRole('tablist')
  await expect(tabs.getByRole('tab', { name: /Inbox/ })).toHaveAttribute('aria-selected', 'true')
  await expect(card(page, 'phone oat milk')).toBeVisible()
  await expect(card(page, 'on the board')).toBeHidden() // other columns are not on screen at the same time
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true) // no sideways page scroll

  await card(page, 'phone oat milk').getByRole('button', { name: 'More actions' }).click()
  await page.getByRole('menuitem', { name: `${fx.pageName} › Done` }).click()
  await expect(card(page, 'phone oat milk')).toBeHidden()
  await tabs.getByRole('tab', { name: /Done/ }).click()
  await expect(card(lane(page, 'Done'), 'phone oat milk')).toBeVisible()

  await page.getByRole('button', { name: 'New note' }).last().click() // the floating button
  await page.getByRole('textbox').fill('from the phone')
  await page.getByRole('button', { name: 'Add note' }).click()
  await expect(card(page, 'from the phone')).toBeVisible()
})
