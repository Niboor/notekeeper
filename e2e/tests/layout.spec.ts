import { expect, test, type Locator, type Page } from '@playwright/test'
import { api, card, createNote, lane, openFreshBoard } from './helpers'

// The layout problems that the unit tests cannot see: menus, dialogs and editors are measured in a real
// browser, in the window the user has (WEB-N1, WEB-N4, WEB-12, WEB-14, WEB-18).

/** True when, for every item that is in view in its menu, the element on top at its centre and near its left edge is the item itself. */
async function everyItemIsOnTop(items: Locator): Promise<boolean> {
  return items.evaluateAll((els) =>
    els.every((el) => {
      const b = el.getBoundingClientRect()
      const menu = el.closest('[role=menu]')!.getBoundingClientRect()
      if (b.bottom > menu.bottom || b.top < menu.top) return true // scrolled out of the menu's own window
      return [b.left + b.width / 2, b.left + 6].every((x) => {
        const top = document.elementFromPoint(x, b.top + b.height / 2)
        return top === el || el.contains(top)
      })
    }),
  )
}

const inViewport = (page: Page, box: { x: number; y: number; width: number; height: number }) => {
  const vp = page.viewportSize()!
  return box.x >= 0 && box.y >= 0 && box.x + box.width <= vp.width && box.y + box.height <= vp.height
}

test('the menu of the first column is not painted over by the Inbox', async ({ page }) => {
  const fx = await openFreshBoard(page, ['Todo', 'Done'])
  await createNote(page, 'a note', fx.columns.Todo)
  await page.getByRole('button', { name: /^Todo: Page options/ }).click()
  const menu = page.getByRole('menu')
  await expect(menu).toBeVisible()
  expect(await everyItemIsOnTop(menu.getByRole('menuitem'))).toBe(true)
  expect(inViewport(page, (await menu.boundingBox())!)).toBe(true)
  // Choosing an item works although the list is not inside the column.
  await menu.getByRole('menuitem', { name: 'Rename column' }).click()
  await expect(lane(page, 'Todo').getByRole('textbox')).toBeFocused()
})

test('the page menu neither hides the tabs nor gets cut off', async ({ page }) => {
  await openFreshBoard(page)
  const tab = page.locator('.page-tabs a[aria-current=page]')
  const before = (await tab.boundingBox())!
  await page.getByRole('button', { name: 'Page options', exact: true }).click()
  const menu = page.getByRole('menu')
  await expect(menu.getByRole('menuitem', { name: 'Delete page' })).toBeVisible()
  expect(await everyItemIsOnTop(menu.getByRole('menuitem'))).toBe(true)
  const after = (await tab.boundingBox())!
  expect(after.y).toBe(before.y)
  await page.keyboard.press('Escape')
  await expect(menu).toBeHidden()
})

test('a card menu stays inside the window, also low on the screen', async ({ page }) => {
  const fx = await openFreshBoard(page)
  for (let i = 0; i < 12; i++) await createNote(page, `filler ${i} ` + 'text '.repeat(20), fx.columns.Todo)
  await page.reload()
  const todo = lane(page, 'Todo')
  await todo.locator('article.note').last().scrollIntoViewIfNeeded()
  const last = todo.locator('article.note').last()
  await last.hover()
  await last.getByRole('button', { name: 'More actions' }).click()
  const menu = page.getByRole('menu')
  await expect(menu).toBeVisible()
  expect(inViewport(page, (await menu.boundingBox())!)).toBe(true)
  expect(await everyItemIsOnTop(menu.getByRole('menuitem'))).toBe(true)
})

test('a long card menu scrolls and stays where it was scrolled to', async ({ page }) => {
  await openFreshBoard(page)
  for (let i = 0; i < 24; i++) {
    const other = await api<{ id: string }>(page, 'POST', '/api/v1/pages', { name: `Elsewhere ${i}` })
    await api(page, 'POST', '/api/v1/categories', { page_id: other.body.id, name: 'Column' }) // a place to move to
  }
  await createNote(page, 'note with a long menu')
  await page.reload()
  const note = card(lane(page, 'Inbox'), 'note with a long menu')
  await note.hover()
  await note.getByRole('button', { name: 'More actions' }).click()
  const menu = page.getByRole('menu')
  expect(await menu.evaluate((el) => el.scrollHeight > el.clientHeight)).toBe(true)
  expect(inViewport(page, (await menu.boundingBox())!)).toBe(true)
  await menu.hover()
  await page.mouse.wheel(0, 300)
  await page.waitForTimeout(400) // the scroll must not send the list back to the top
  expect(await menu.evaluate((el) => el.scrollTop)).toBeGreaterThan(100)
})

test('the editor grows with the note and gives the focus back to the card', async ({ page }) => {
  await openFreshBoard(page)
  const text = Array.from({ length: 9 }, (_, i) => `line ${i + 1}`).join('\n')
  await createNote(page, text)
  await page.reload()
  const note = card(lane(page, 'Inbox'), 'line 1')
  await note.hover()
  await note.getByRole('button', { name: 'More actions' }).click()
  await page.getByRole('menuitem', { name: 'Edit' }).click()
  const editor = note.getByRole('textbox')
  await expect(editor).toBeFocused()
  const fits = () => editor.evaluate((el) => el.scrollHeight <= el.clientHeight + 1)
  expect(await fits()).toBe(true)
  await editor.pressSequentially('\nmore\nand more\nand more still')
  expect(await fits()).toBe(true)
  await page.keyboard.press('Escape')
  await expect(note).toBeFocused()
})

test('the composer grows as well', async ({ page }) => {
  await openFreshBoard(page)
  await page.getByRole('button', { name: 'New note' }).first().click()
  const box = page.locator('.composer textarea')
  await box.fill(Array.from({ length: 8 }, (_, i) => `- [ ] item ${i}`).join('\n'))
  expect(await box.evaluate((el) => el.scrollHeight <= el.clientHeight + 1)).toBe(true)
})

test('the reminder dialog fits its window', async ({ page }) => {
  await openFreshBoard(page)
  await createNote(page, 'remind me')
  await page.reload()
  const note = card(lane(page, 'Inbox'), 'remind me')
  await note.hover()
  await note.getByRole('button', { name: 'More actions' }).click()
  await page.getByRole('menuitem', { name: 'Remind me…' }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible()
  const set = dialog.getByRole('button', { name: 'Set reminder' })
  const d = (await dialog.boundingBox())!
  const b = (await set.boundingBox())!
  expect(b.x + b.width).toBeLessThanOrEqual(d.x + d.width)
  expect(await dialog.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true)
})

test('search: the box is empty away from the search page, results are counted and long notes folded', async ({ page }) => {
  await openFreshBoard(page)
  await createNote(page, 'short pineapple note')
  await createNote(page, 'a long pineapple note ' + 'padding '.repeat(60))
  await page.goto('/search?q=pineapple')
  await expect(page.getByText('2 results')).toBeVisible()
  await expect(page.getByRole('searchbox')).toHaveValue('pineapple')
  await expect(page.locator('details.whole-note')).toHaveCount(1) // only the long note has its text folded away
  await expect(page.getByText('short pineapple note')).toBeVisible()
  await page.goto('/settings')
  await expect(page.getByRole('searchbox')).toHaveValue('')
})

test('an address that leads nowhere says so', async ({ page }) => {
  await page.goto('/no/such/place')
  await expect(page.getByRole('heading', { name: 'That page does not exist' })).toBeVisible()
  await page.getByRole('link', { name: 'Back to your notes' }).click()
  await expect(page).toHaveURL(/\/(p\/.*)?$/)
})

test('a checklist ticks when its words are clicked', async ({ page }) => {
  await openFreshBoard(page)
  const id = await createNote(page, '- [ ] eggs\n- [ ] milk')
  await page.reload()
  await card(lane(page, 'Inbox'), 'eggs').getByText('milk').click()
  await expect.poll(async () => (await api<{ parts: { text: string }[] }>(page, 'GET', `/api/v1/notes/${id}`)).body.parts[0]!.text).toBe('- [ ] eggs\n- [x] milk')
})

test('settings and administration use the same controls as the rest of the app', async ({ page }) => {
  await page.goto('/settings')
  const select = page.getByRole('combobox', { name: 'Time zone' })
  const password = page.getByLabel('Current password')
  const s = await select.evaluate((el) => ({ h: el.getBoundingClientRect().height, r: getComputedStyle(el).borderRadius }))
  const p = await password.evaluate((el) => ({ h: el.getBoundingClientRect().height, r: getComputedStyle(el).borderRadius }))
  expect(s.r).toBe(p.r)
  expect(Math.abs(s.h - p.h)).toBeLessThanOrEqual(4)
  expect(await page.getByRole('checkbox').first().evaluate((el) => getComputedStyle(el).accentColor)).not.toBe('auto')

  await page.goto('/admin')
  for (const name of ['New username', 'Name', 'Chat identity domain (e.g. example.org)']) {
    const box = await page.getByRole('textbox', { name, exact: true }).boundingBox()
    expect(box!.height).toBeGreaterThanOrEqual(32)
  }
})

test('the page tabs scroll without a scrollbar and the page tools stay in reach', async ({ page }) => {
  await openFreshBoard(page)
  for (let i = 0; i < 12; i++) await api(page, 'POST', '/api/v1/pages', { name: `A rather long page name number ${i}` })
  await page.reload()
  const strip = page.locator('.page-tabs')
  expect(await strip.evaluate((el) => el.scrollWidth > el.clientWidth)).toBe(true)
  expect(await strip.evaluate((el) => (el as HTMLElement).offsetHeight === el.clientHeight)).toBe(true) // no scrollbar takes room
  for (const name of ['Page options', 'New page']) {
    expect(inViewport(page, (await page.getByRole('button', { name, exact: true }).boundingBox())!)).toBe(true)
  }
})
