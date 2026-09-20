import { expect, test } from '@playwright/test'
import { card, lane } from './helpers'

// The first-run path: an account with nothing in it guides the user to create a page (CORE-P5),
// then a column, then a first note (F6). The board shows a page's columns (WEB-1) beside the
// Inbox tray (WEB-2).
test('a new account is guided from an empty page to a first note @cross', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByText('Make your first page')).toBeVisible()

  await page.getByRole('button', { name: 'New page' }).click()
  await page.getByPlaceholder('Page name').fill('Work')
  await page.keyboard.press('Enter')
  await expect(page.getByRole('link', { name: 'Work' })).toBeVisible()

  await page.getByRole('button', { name: 'Add a column' }).click()
  await page.getByPlaceholder('Column name').fill('This week')
  await page.keyboard.press('Enter')
  const week = lane(page, 'This week')
  await expect(week).toBeVisible()

  await week.getByRole('button', { name: 'Add a note' }).click()
  await week.getByRole('textbox').fill('Concert Friday\n\n- [ ] tickets\n- [ ] train times')
  await page.keyboard.press('Control+Enter')
  await expect(card(week, 'Concert Friday')).toBeVisible()
  await expect(card(week, 'Concert Friday').getByRole('checkbox', { name: 'tickets' })).toBeVisible()

  // It survives a reload: it is on the server, not just on the screen.
  await page.reload()
  await expect(card(lane(page, 'This week'), 'Concert Friday')).toBeVisible()
})
