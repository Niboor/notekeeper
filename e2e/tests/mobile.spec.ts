import { devices, expect, test } from '@playwright/test'
import { api, card, createNote, lane, openFreshBoard } from './helpers'

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

// A menu is a bottom sheet that fits the screen, however many places a note can move to.
test('the card menu is a sheet inside the screen and every move target can be reached', async ({ page }) => {
  await openFreshBoard(page)
  for (let i = 0; i < 6; i++) await api(page, 'POST', '/api/v1/pages', { name: `Another page ${i}` })
  await createNote(page, 'phone menu note')
  await page.reload()
  await card(page, 'phone menu note').getByRole('button', { name: 'More actions' }).click()
  const menu = page.getByRole('menu')
  await expect(menu).toBeVisible()
  const vp = page.viewportSize()!
  const box = (await menu.boundingBox())!
  expect(box.y).toBeGreaterThanOrEqual(0)
  expect(box.y + box.height).toBeLessThanOrEqual(vp.height + 1)
  expect(box.x).toBeGreaterThanOrEqual(0)
  expect(box.x + box.width).toBeLessThanOrEqual(vp.width + 1)
  const items = menu.getByRole('menuitem')
  await items.last().scrollIntoViewIfNeeded()
  const last = (await items.last().boundingBox())!
  expect(last.y + last.height).toBeLessThanOrEqual(vp.height)
})

// A reminder at a chosen time can be set on a phone: the button is inside the dialog.
test('the reminder dialog fits a phone and sets a reminder at a chosen time', async ({ page }) => {
  await openFreshBoard(page)
  await createNote(page, 'phone reminder')
  await page.reload()
  await card(page, 'phone reminder').getByRole('button', { name: 'More actions' }).click()
  await page.getByRole('menuitem', { name: 'Remind me…' }).click()
  const dialog = page.getByRole('dialog')
  const set = dialog.getByRole('button', { name: 'Set reminder' })
  await expect(set).toBeVisible()
  const vp = page.viewportSize()!
  const b = (await set.boundingBox())!
  expect(b.x).toBeGreaterThanOrEqual(0)
  expect(b.x + b.width).toBeLessThanOrEqual(vp.width)
  expect(await dialog.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true)
  await set.click()
  await expect(dialog.getByText(/^Due /)).toBeVisible()
})

// The page menu and "new page" are on the screen, not behind a sideways scroll of the tabs.
test('page options and new page are on screen next to a long list of tabs', async ({ page }) => {
  await openFreshBoard(page)
  for (let i = 0; i < 6; i++) await api(page, 'POST', '/api/v1/pages', { name: `A rather long page name number ${i}` })
  await page.reload()
  const vp = page.viewportSize()!
  for (const name of ['Page options', 'New page']) {
    const b = (await page.getByRole('button', { name }).boundingBox())!
    expect(b.x + b.width).toBeLessThanOrEqual(vp.width)
    expect(b.width).toBeGreaterThanOrEqual(38) // a finger-sized target
  }
})
