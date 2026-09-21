import { expect, test, type Locator, type Page } from '@playwright/test'
import { api, card, createNote, lane, openFreshBoard } from './helpers'

// WEB-22, CORE-P2, CORE-P3, WEB-5: columns are moved by dragging their title, within a page and to another page, and
// through the column menu (WEB-5).

interface ColumnJson { category: { id: string; name: string; page_id: string } }

/** The column names of a page in the order the server has them. */
const serverOrder = async (page: Page, pageId: string) =>
  (await api<{ categories: ColumnJson[] }>(page, 'GET', `/api/v1/pages/${pageId}/board`)).body.categories.map((c) => c.category.name)

/** The column names as shown, left to right. */
const shownOrder = (page: Page) => page.locator('.board .lane:not(.add-lane) h2').allInnerTexts()

const title = (page: Page, name: string): Locator => lane(page, name).locator('.lane-grip')

/** Drags a column by its title with real mouse events to a point, optionally holding there. */
async function dragColumn(page: Page, name: string, to: { x: number; y: number }, opts: { hold?: number; release?: boolean } = {}) {
  const box = (await title(page, name).boundingBox())!
  await page.mouse.move(box.x + 12, box.y + box.height / 2)
  await page.mouse.down()
  await page.mouse.move(box.x + 30, box.y + box.height / 2 + 8, { steps: 4 }) // past the 5 px activation distance
  await page.mouse.move(to.x, to.y, { steps: 20 })
  if (opts.hold) await page.waitForTimeout(opts.hold)
  if (opts.release !== false) await page.mouse.up()
}

/** A point in the left or right part of a column, below its title. */
async function inColumn(page: Page, name: string, side: 'left' | 'right') {
  const b = (await lane(page, name).boundingBox())!
  return { x: b.x + (side === 'left' ? b.width * 0.2 : b.width * 0.8), y: b.y + 120 }
}

test('drag a column to another place on its page @cross', async ({ page }) => {
  const fx = await openFreshBoard(page, ['A', 'B', 'C'])
  await createNote(page, 'note in A', fx.columns.A)
  await page.reload()
  await expect.poll(() => shownOrder(page)).toEqual(['A', 'B', 'C'])

  await dragColumn(page, 'A', await inColumn(page, 'C', 'right')) // after C
  await expect.poll(() => shownOrder(page)).toEqual(['B', 'C', 'A'])
  await expect.poll(() => serverOrder(page, fx.pageId)).toEqual(['B', 'C', 'A'])
  await expect(card(lane(page, 'A'), 'note in A')).toBeVisible() // its notes went with it

  await dragColumn(page, 'C', await inColumn(page, 'B', 'left')) // before B
  await expect.poll(() => serverOrder(page, fx.pageId)).toEqual(['C', 'B', 'A'])
  await page.reload()
  await expect.poll(() => shownOrder(page)).toEqual(['C', 'B', 'A'])
})

test('a column dropped where it was changes nothing', async ({ page }) => {
  const fx = await openFreshBoard(page, ['A', 'B', 'C'])
  const before = await page.evaluate(() => performance.now())
  await dragColumn(page, 'B', await inColumn(page, 'B', 'right')) // over itself
  await page.waitForTimeout(500)
  expect(await shownOrder(page)).toEqual(['A', 'B', 'C'])
  expect(await serverOrder(page, fx.pageId)).toEqual(['A', 'B', 'C'])
  expect(before).toBeGreaterThan(0)
})

test('the Inbox stays where it is', async ({ page }) => {
  await openFreshBoard(page, ['A', 'B'])
  await expect(lane(page, 'Inbox').locator('.lane-grip')).not.toHaveClass(/can-drag/)
})

const unique = (name: string) => `${name} ${Math.random().toString(36).slice(2, 6)}`

test('drop a column on another page\'s tab: it goes to the end of that page, and Undo brings it back', async ({ page }) => {
  const fx = await openFreshBoard(page, ['A', 'B', 'C'])
  await createNote(page, 'travels with A', fx.columns.A)
  const name = unique('Elsewhere')
  const other = await api<{ id: string }>(page, 'POST', '/api/v1/pages', { name })
  await api(page, 'POST', '/api/v1/categories', { page_id: other.body.id, name: 'X' })
  await page.reload()

  const tab = page.getByRole('link', { name })
  await tab.scrollIntoViewIfNeeded()
  const t = (await tab.boundingBox())!
  await dragColumn(page, 'A', { x: t.x + t.width / 2, y: t.y + t.height / 2 }) // dropped at once, no hold
  await expect(lane(page, 'A')).toHaveCount(0)
  await expect.poll(() => serverOrder(page, other.body.id)).toEqual(['X', 'A'])
  await expect.poll(() => serverOrder(page, fx.pageId)).toEqual(['B', 'C'])
  await expect(page.getByText(`Column "A" moved to ${name}`)).toBeVisible()

  await page.getByRole('button', { name: 'Undo' }).click()
  await expect.poll(() => serverOrder(page, fx.pageId)).toEqual(['A', 'B', 'C'])
  await expect.poll(() => serverOrder(page, other.body.id)).toEqual(['X'])
  await expect(card(lane(page, 'A'), 'travels with A')).toBeVisible()
})

test('hold a column over another page\'s tab to open it, then drop it between its columns', async ({ page }) => {
  const fx = await openFreshBoard(page, ['A', 'B'])
  const name = unique('Elsewhere')
  const other = await api<{ id: string }>(page, 'POST', '/api/v1/pages', { name })
  for (const column of ['X', 'Y', 'Z']) await api(page, 'POST', '/api/v1/categories', { page_id: other.body.id, name: column })
  await page.reload()

  const tab = page.getByRole('link', { name })
  await tab.scrollIntoViewIfNeeded()
  const t = (await tab.boundingBox())!
  await dragColumn(page, 'A', { x: t.x + t.width / 2, y: t.y + t.height / 2 }, { hold: 1000, release: false })
  await expect(page).toHaveURL(new RegExp(`/p/${other.body.id}$`))
  await expect(lane(page, 'Y')).toBeVisible()
  const y = await inColumn(page, 'Y', 'right') // after Y
  await page.mouse.move(y.x, y.y, { steps: 15 })
  await page.mouse.up()
  await expect.poll(() => serverOrder(page, other.body.id)).toEqual(['X', 'Y', 'A', 'Z'])
  await expect.poll(() => serverOrder(page, fx.pageId)).toEqual(['B'])
  await expect.poll(() => shownOrder(page)).toEqual(['X', 'Y', 'A', 'Z'])
})

test('a column can be dropped on a page that has none', async ({ page }) => {
  const fx = await openFreshBoard(page, ['A', 'B'])
  for (let i = 0; i < 8; i++) await api(page, 'POST', '/api/v1/pages', { name: `Filler page ${i}` }) // the tab strip overflows
  const name = unique('Empty')
  const empty = await api<{ id: string }>(page, 'POST', '/api/v1/pages', { name })
  await page.reload()
  const tab = page.getByRole('link', { name })
  await tab.scrollIntoViewIfNeeded()
  const t = (await tab.boundingBox())!
  await dragColumn(page, 'B', { x: t.x + t.width / 2, y: t.y + t.height / 2 }, { hold: 1000, release: false })
  await expect(page).toHaveURL(new RegExp(`/p/${empty.body.id}$`))
  const add = (await page.locator('.add-lane').boundingBox())! // the tile after the last column is the drop zone
  await page.mouse.move(add.x + add.width / 2, add.y + 20, { steps: 15 })
  await page.mouse.up()
  await expect.poll(() => serverOrder(page, empty.body.id)).toEqual(['B'])
  await expect.poll(() => serverOrder(page, fx.pageId)).toEqual(['A'])
})

test('the column menu moves a column left and right, and to another page @cross', async ({ page }) => {
  const fx = await openFreshBoard(page, ['A', 'B', 'C'])
  const name = unique('Elsewhere')
  const other = await api<{ id: string }>(page, 'POST', '/api/v1/pages', { name })
  await page.reload()

  const menuOf = (name: string) => page.getByRole('button', { name: new RegExp(`^${name}: Page options`) })
  await menuOf('B').click()
  await expect(page.getByRole('menuitem', { name: 'Move left' })).toBeVisible()
  await page.getByRole('menuitem', { name: 'Move right' }).click()
  await expect.poll(() => serverOrder(page, fx.pageId)).toEqual(['A', 'C', 'B'])
  await expect.poll(() => shownOrder(page)).toEqual(['A', 'C', 'B'])

  await menuOf('A').click() // the first column cannot move left, the last cannot move right
  await expect(page.getByRole('menuitem', { name: 'Move left' })).toHaveCount(0)
  await expect(page.getByRole('menuitem', { name: 'Move right' })).toBeVisible()
  await page.keyboard.press('Escape')
  await menuOf('B').click()
  await expect(page.getByRole('menuitem', { name: 'Move right' })).toHaveCount(0)
  await page.keyboard.press('Escape')

  await menuOf('C').click()
  await page.getByRole('menuitem', { name, exact: true }).click()
  await expect.poll(() => serverOrder(page, other.body.id)).toEqual(['C'])
  await expect.poll(() => serverOrder(page, fx.pageId)).toEqual(['A', 'B'])
})

test('other browsers see a moved column at once', async ({ page, browser }) => {
  const fx = await openFreshBoard(page, ['A', 'B', 'C'])
  const second = await browser.newContext({ storageState: 'auth.json' })
  const view = await second.newPage()
  await view.goto(`/p/${fx.pageId}`)
  await expect(lane(view, 'C')).toBeVisible()
  await dragColumn(page, 'A', await inColumn(page, 'C', 'right'))
  await expect.poll(() => shownOrder(view), { timeout: 10_000 }).toEqual(['B', 'C', 'A'])
  await second.close()
})
