import { expect, test } from '@playwright/test'
import { api, botSend, card, createNote, dragTo, lane, openFreshBoard } from './helpers'

// F2 / WEB-11: a message arriving through the bot API shows up in the Inbox within seconds,
// without a reload.
test('a chat message appears live in the Inbox @cross', async ({ page }) => {
  await openFreshBoard(page)
  await botSend('buy oat milk (live)')
  await expect(card(lane(page, 'Inbox'), 'buy oat milk (live)')).toBeVisible({ timeout: 10_000 })
})

// F6 / WEB-3: dragging with the mouse from the Inbox into a column, then reordering inside it;
// the result is on the server (a reload shows it).
test('drag with the mouse between lanes and within a lane', async ({ page }) => {
  const fx = await openFreshBoard(page)
  const a = await createNote(page, 'first note')
  await createNote(page, 'second note', fx.columns.Todo)
  await page.reload()
  const inbox = lane(page, 'Inbox')
  await expect(card(inbox, 'first note')).toBeVisible()

  await dragTo(page, card(inbox, 'first note'), card(lane(page, 'Todo'), 'second note'))
  await expect(card(lane(page, 'Todo'), 'first note')).toBeVisible()
  await expect(card(inbox, 'first note')).toHaveCount(0)

  await page.reload()
  await expect(lane(page, 'Todo').locator('article.note')).toHaveCount(2) // wait for the board to load before reading it
  const titles = await lane(page, 'Todo').locator('article.note').allInnerTexts()
  expect(titles).toHaveLength(2)
  expect((await api<{ category_id: string }>(page, 'GET', `/api/v1/notes/${a}`)).body.category_id).toBe(fx.columns.Todo)

  // Reorder: drag the lower card above the upper one.
  const todo = lane(page, 'Todo')
  const order = async () => (await todo.locator('article.note').allInnerTexts()).map((t) => t.split('\n')[0])
  const before = await order()
  await dragTo(page, todo.locator('article.note').nth(1), todo.locator('article.note').nth(0))
  await expect.poll(order).toEqual([before[1], before[0]])
  await page.reload()
  expect(await order()).toEqual([before[1], before[0]])
})

// WEB-5: the keyboard alternative. Space picks a note up, the arrow keys move it, Space drops it,
// and every step is announced in a live region.
test('move a note with the keyboard and hear it announced', async ({ page }) => {
  const fx = await openFreshBoard(page)
  await createNote(page, 'keyboard note', fx.columns.Todo)
  await createNote(page, 'second', fx.columns.Todo)
  await page.reload()
  const announcer = page.getByTestId('dnd-announcer')
  const note = card(lane(page, 'Todo'), 'keyboard note')
  await note.focus()
  await page.keyboard.press('Space')
  await expect(announcer).toContainText('Picked up')
  await page.keyboard.press('ArrowUp') // within the lane (it is the lower of the two)
  await expect(announcer).toContainText('Moved to Todo, position 1 of 2')
  await page.keyboard.press('ArrowRight') // into the next lane, which is empty
  await expect(announcer).toContainText('Moved to Done, position 1 of 1')
  await page.keyboard.press('Space')
  await expect(announcer).toContainText('Dropped in Done')
  await expect(card(lane(page, 'Done'), 'keyboard note')).toBeVisible()
  await page.reload()
  await expect(card(lane(page, 'Done'), 'keyboard note')).toBeVisible()

  // Escape puts a lifted note back.
  const other = card(lane(page, 'Todo'), 'second')
  await other.focus()
  await page.keyboard.press('Space')
  await page.keyboard.press('ArrowRight')
  await page.keyboard.press('Escape')
  await expect(announcer).toContainText('Cancelled')
  await expect(card(lane(page, 'Todo'), 'second')).toBeVisible()
})

// WEB-4: holding a dragged note over another page's tab opens that page so it can be dropped there.
test('hold a note over a page tab to move it to another page', async ({ page }) => {
  const one = await openFreshBoard(page, ['Todo'])
  await createNote(page, 'travelling note', one.columns.Todo)
  const two = await api<{ id: string }>(page, 'POST', '/api/v1/pages', { name: 'Elsewhere' })
  const target = await api<{ id: string }>(page, 'POST', '/api/v1/categories', { page_id: two.body.id, name: 'Arrivals' })
  await page.reload()

  const note = card(lane(page, 'Todo'), 'travelling note')
  const tab = page.getByRole('link', { name: 'Elsewhere' })
  await tab.scrollIntoViewIfNeeded() // with many pages the strip scrolls; a person brings the tab into view first
  const a = (await note.boundingBox())!
  const t = (await tab.boundingBox())!
  await page.mouse.move(a.x + 40, a.y + 20)
  await page.mouse.down()
  await page.mouse.move(a.x + 55, a.y + 30, { steps: 4 })
  await page.mouse.move(t.x + t.width / 2, t.y + t.height / 2, { steps: 15 })
  await page.waitForTimeout(1000) // "hold to open"
  await expect(page).toHaveURL(new RegExp(`/p/${two.body.id}$`))
  const arrivals = lane(page, 'Arrivals')
  await expect(arrivals).toBeVisible()
  const b = (await arrivals.boundingBox())!
  await page.mouse.move(b.x + b.width / 2, b.y + 60, { steps: 15 })
  await page.mouse.up()
  await expect(card(arrivals, 'travelling note')).toBeVisible()
  // The card shows at once; the move reaches the server a moment later.
  await expect
    .poll(async () => (await api<{ items: { category_id: string }[] }>(page, 'GET', `/api/v1/categories/${target.body.id}/notes`)).body.items.length)
    .toBe(1)
})

// WEB-6, CORE-N8, WEB-7: dismiss with one click, undo, and the Trash.
test('dismiss with undo, then restore or delete for good from the Trash @cross', async ({ page }) => {
  const fx = await openFreshBoard(page)
  await createNote(page, 'to dismiss', fx.columns.Todo)
  await createNote(page, 'to delete', fx.columns.Todo)
  await page.reload()
  const todo = lane(page, 'Todo')

  await card(todo, 'to dismiss').getByRole('button', { name: 'Dismiss' }).click()
  await expect(card(todo, 'to dismiss')).toHaveCount(0) // no confirmation dialog
  await page.getByRole('button', { name: 'Undo' }).click()
  await expect(card(todo, 'to dismiss')).toBeVisible()

  await card(todo, 'to delete').getByRole('button', { name: 'Dismiss' }).click()
  await card(todo, 'to dismiss').getByRole('button', { name: 'Dismiss' }).click()
  await page.getByRole('link', { name: 'Trash' }).click()
  await expect(page.getByText(`From ${fx.pageName} › Todo`).first()).toBeVisible()
  await page.locator('article.note', { hasText: 'to dismiss' }).getByRole('button', { name: 'Restore' }).click()
  page.once('dialog', (d) => void d.accept())
  await page.locator('article.note', { hasText: 'to delete' }).getByRole('button', { name: 'Delete for good' }).click()
  await expect(page.locator('article.note', { hasText: 'to delete' })).toHaveCount(0)
  await page.goBack()
  await expect(card(lane(page, 'Todo'), 'to dismiss')).toBeVisible() // restored where it was
})

// CORE-N5, CORE-N17, WEB-20: editing text, and toggling a checklist box without entering edit mode.
test('edit a note and tick a checklist item @cross', async ({ page }) => {
  const fx = await openFreshBoard(page)
  await createNote(page, 'plan\n\n- [ ] one\n- [ ] two', fx.columns.Todo)
  await page.reload()
  const note = card(lane(page, 'Todo'), 'plan')
  await note.getByRole('checkbox', { name: 'two' }).check()
  await expect(note.getByRole('checkbox', { name: 'two' })).toBeChecked()
  await page.reload()
  await expect(card(lane(page, 'Todo'), 'plan').getByRole('checkbox', { name: 'two' })).toBeChecked()

  await note.getByRole('button', { name: 'More actions' }).click()
  await page.getByRole('menuitem', { name: 'Edit' }).click()
  const box = note.getByRole('textbox')
  await box.fill('plan (edited)\n\n- [ ] one\n- [x] two')
  await page.keyboard.press('Control+Enter')
  await expect(card(lane(page, 'Todo'), 'plan (edited)')).toBeVisible()
})

// WEB-14, CORE-N13: global search across pages and the Trash.
test('search finds notes anywhere and jumps to them @cross', async ({ page }) => {
  const fx = await openFreshBoard(page)
  await createNote(page, 'Extraordinary concert tickets', fx.columns.Todo)
  await createNote(page, 'unrelated shopping list')
  await page.reload()
  await page.keyboard.press('/')
  await page.getByRole('searchbox', { name: 'Search notes' }).fill('conc')
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(/\/search\?q=conc/)
  const hit = page.locator('article.note', { hasText: 'Extraordinary concert tickets' })
  await expect(hit).toBeVisible()
  await expect(page.locator('article.note', { hasText: 'shopping list' })).toHaveCount(0)
  await hit.getByRole('link', { name: new RegExp(`in ${fx.pageName}`) }).click()
  await expect(card(lane(page, 'Todo'), 'Extraordinary concert tickets')).toBeVisible()
})

// CORE-A1, CORE-A6: attach a file in the composer; it is stored, shown and downloadable.
// WEB-8: attachments show on the card and download again.
test('attach a file to a new note and download it again @cross', async ({ page }) => {
  const fx = await openFreshBoard(page)
  const todo = lane(page, 'Todo')
  await todo.getByRole('button', { name: 'Add a note' }).click()
  await todo.locator('input[type=file]').setInputFiles({ name: 'ticket.txt', mimeType: 'text/plain', buffer: Buffer.from('gate C, row 12') })
  await expect(todo.getByText('ticket.txt')).toBeVisible()
  await todo.getByRole('textbox').fill('concert ticket')
  await expect(todo.getByRole('button', { name: 'Add note' })).toBeEnabled() // the file has finished uploading; before that adding waits
  await page.keyboard.press('Control+Enter')
  const note = card(todo, 'concert ticket')
  await expect(note.getByRole('link', { name: /ticket.txt/ })).toBeVisible()
  const link = await note.getByRole('link', { name: /ticket.txt/ }).getAttribute('href')
  const download = await page.evaluate(async (href) => {
    const res = await fetch(href)
    return { status: res.status, text: await res.text(), disposition: res.headers.get('Content-Disposition') }
  }, link!)
  expect(download.status).toBe(200)
  expect(download.text).toBe('gate C, row 12')
  expect(download.disposition).toContain('ticket.txt')
  void fx
})

// CORE-N14, GRP-10, WEB-15, WEB-13: correct a grouping in the app (merge two notes, split one out) and
// go back to an earlier text from the history.
test('merge two notes, split one out, and restore an earlier text', async ({ page }) => {
  const fx = await openFreshBoard(page)
  const a = await createNote(page, 'merge one', fx.columns.Todo)
  await createNote(page, 'merge two', fx.columns.Todo)
  await page.reload()
  const todo = lane(page, 'Todo')
  await expect(todo.locator('article.note')).toHaveCount(2)

  await card(todo, 'merge one').getByRole('button', { name: 'More actions' }).click()
  await page.getByRole('menuitem', { name: 'Merge another note into this one…' }).click()
  const dialog = page.getByRole('dialog', { name: 'Merge notes' })
  await dialog.getByRole('button', { name: 'Merge', exact: true }).click()
  await expect(todo.locator('article.note')).toHaveCount(1)
  await expect(card(todo, 'merge one')).toContainText('merge two')

  await card(todo, 'merge one').getByRole('button', { name: 'More actions' }).click()
  await page.getByRole('menuitem', { name: /Split out as a note of its own: merge two/ }).click()
  await expect(todo.locator('article.note')).toHaveCount(2)
  await expect(card(todo, 'merge two')).not.toContainText('merge one')

  // History: an edit in the app, then back to the first text.
  await api(page, 'PATCH', `/api/v1/notes/${a}/parts/${(await api<{ parts: { id: string }[] }>(page, 'GET', `/api/v1/notes/${a}`)).body.parts[0]!.id}`, { text: 'merge one, edited' })
  await expect(card(todo, 'merge one, edited')).toBeVisible()
  await card(todo, 'merge one, edited').getByRole('button', { name: 'More actions' }).click()
  await page.getByRole('menuitem', { name: 'History…' }).click()
  const history = page.getByRole('dialog', { name: 'Earlier versions' })
  await history.getByRole('button', { name: /Restore this version/ }).first().click()
  await expect(card(todo, 'merge one')).toBeVisible()
  await expect(card(todo, 'merge one, edited')).toHaveCount(0)
})

// WEB-N8: the app is installable and its shell opens without a network; nothing from the API is cached.
test('the app shell works offline and the API is never cached', async ({ page, context }) => {
  await openFreshBoard(page)
  await page.evaluate(async () => (await navigator.serviceWorker.ready).active?.state)
  const manifest = await (await page.request.get('/manifest.webmanifest')).json()
  expect(manifest.display).toBe('standalone')
  expect(manifest.icons.some((i: { sizes: string }) => i.sizes === '512x512')).toBe(true)
  await page.reload() // now controlled by the worker
  await expect(page.getByRole('button', { name: 'New note' })).toBeVisible()
  const cached = await page.evaluate(async () => {
    const urls: string[] = []
    for (const name of await caches.keys()) for (const req of await (await caches.open(name)).keys()) urls.push(new URL(req.url).pathname)
    return urls
  })
  expect(cached.length).toBeGreaterThan(0)
  expect(cached.filter((u) => u.startsWith('/api/'))).toEqual([])
  await context.setOffline(true)
  await page.reload()
  await expect(page.getByText(/offline/i).first()).toBeVisible({ timeout: 15_000 }) // the shell renders and says so
  await context.setOffline(false)
})

// WEB-N3: the first screen is usable within two seconds, and an action shows its effect within 100 ms,
// before the server has answered (optimistic updates).
test('the app opens quickly and reacts instantly', async ({ page }) => {
  const fx = await openFreshBoard(page)
  await createNote(page, 'speed test one', fx.columns.Todo)
  await createNote(page, 'speed test two', fx.columns.Todo)
  const started = Date.now()
  await page.reload()
  await expect(card(lane(page, 'Todo'), 'speed test one')).toBeVisible()
  expect(Date.now() - started).toBeLessThan(2000)

  // Slow the server down so that only an optimistic update can explain a fast reaction.
  await page.route('**/api/v1/notes/*/dismiss', async (route) => {
    await new Promise((r) => setTimeout(r, 1500))
    await route.continue()
  })
  const took = await page.evaluate(async () => {
    const el = document.querySelector('article.note[data-id]')!
    const gone = new Promise<number>((resolve) => {
      const t0 = performance.now()
      new MutationObserver((_, obs) => {
        if (!document.body.contains(el)) {
          obs.disconnect()
          resolve(performance.now() - t0)
        }
      }).observe(document.body, { childList: true, subtree: true })
      ;(el.querySelector('button.dismiss') as HTMLButtonElement).click()
    })
    return gone
  })
  expect(took).toBeLessThan(100)
})

// WEB-16: the keyboard reaches the common actions: "/" searches, "n" writes a note, Escape leaves.
test('keyboard shortcuts open search and the composer', async ({ page }) => {
  await openFreshBoard(page)
  await page.keyboard.press('/')
  await expect(page.getByRole('searchbox')).toBeFocused()
  await page.keyboard.press('Escape')
  await page.locator('body').click({ position: { x: 5, y: 300 } })
  await page.keyboard.press('n')
  await expect(page.getByRole('textbox').first()).toBeFocused()
})
