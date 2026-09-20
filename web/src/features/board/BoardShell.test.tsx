import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Route, Routes } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cancelScheduledRefresh } from '../../api/session'
import { fakeFetch, me, renderApp } from '../../test/helpers'
import { BoardShell } from './BoardShell'

afterEach(() => {
  cancelScheduledRefresh()
  vi.unstubAllGlobals()
})

const now = new Date().toISOString()
const note = (id: string, text: string, category: string | null = null) => ({
  id, category_id: category, state: 'active', created_at: now, updated_at: now, version: 1,
  parts: [{ id: id + 'p', kind: 'text', text, attach_reason: 'app', created_at: now }],
})

const pages = { items: [{ id: 'p1', name: 'Work', version: 1, categories: [{ id: 'c1', name: 'Todo' }, { id: 'c2', name: 'Done' }] }] }
const board = {
  page: { id: 'p1', name: 'Work', version: 1 }, inbox_total: 2,
  categories: [
    { category: { id: 'c1', page_id: 'p1', name: 'Todo', version: 1 }, notes: [note('n1', 'write report', 'c1')], total: 1 },
    { category: { id: 'c2', page_id: 'p1', name: 'Done', version: 1 }, notes: [], total: 0 },
  ],
}

/** A tiny in-memory server: reads reflect earlier writes, as the real one does. */
function server(overrides: Parameters<typeof fakeFetch>[0] = []) {
  const state = {
    inbox: [note('i1', 'buy milk'), note('i2', '- [ ] eggs\n- [x] bread')] as ReturnType<typeof note>[],
    lanes: { c1: [note('n1', 'write report', 'c1')], c2: [] as ReturnType<typeof note>[] } as Record<string, ReturnType<typeof note>[]>,
  }
  const find = (id: string) => [...state.inbox, ...state.lanes.c1!, ...state.lanes.c2!].find((n) => n.id === id)
  const detach = (id: string) => {
    state.inbox = state.inbox.filter((n) => n.id !== id)
    for (const k of Object.keys(state.lanes)) state.lanes[k] = state.lanes[k]!.filter((n) => n.id !== id)
  }
  const f = fakeFetch([
    ...overrides,
    { path: '/api/v1/me', body: me },
    { path: '/api/v1/pages', body: pages },
    {
      path: '/api/v1/pages/p1/board',
      handler: () => ({
        ...board,
        inbox_total: state.inbox.length,
        categories: board.categories.map((c) => ({ ...c, notes: state.lanes[c.category.id], total: state.lanes[c.category.id]!.length })),
      }),
    },
    { path: '/api/v1/inbox/notes', handler: () => ({ items: state.inbox, total: state.inbox.length }) },
    {
      method: 'POST',
      path: '/api/v1/notes',
      status: 201,
      handler: async (r) => {
        const body = (await r.clone().json()) as { id: string; category_id: string | null; parts: { text: string }[] }
        const created = note(body.id, body.parts[0]!.text, body.category_id)
        if (body.category_id) state.lanes[body.category_id]!.unshift(created)
        else state.inbox.unshift(created)
        return created
      },
    },
    {
      method: 'POST',
      path: /\/api\/v1\/notes\/[^/]+\/dismiss$/,
      handler: (r) => {
        const id = new URL(r.url).pathname.split('/')[4]!
        const n = find(id)
        detach(id)
        return { ...n, state: 'deleted' }
      },
    },
    {
      method: 'POST',
      path: /\/api\/v1\/notes\/[^/]+\/restore$/,
      handler: () => note('i1', 'buy milk'),
    },
    {
      method: 'POST',
      path: /\/api\/v1\/notes\/[^/]+\/move$/,
      handler: async (r) => {
        const id = new URL(r.url).pathname.split('/')[4]!
        const body = (await r.clone().json()) as { category_id: string | null }
        const n = find(id)!
        detach(id)
        const moved = { ...n, category_id: body.category_id }
        if (body.category_id) state.lanes[body.category_id]!.unshift(moved)
        else state.inbox.unshift(moved)
        return moved
      },
    },
  ])
  return f
}

const shell = () =>
  renderApp(
    <Routes>
      <Route path="/p/:pageId" element={<BoardShell />} />
    </Routes>,
    { route: '/p/p1' },
  )

describe('BoardShell', () => {
  it('shows the Inbox tray and the columns of the page with their counts', async () => {
    vi.stubGlobal('fetch', server())
    shell()
    expect(await screen.findByText('write report')).toBeInTheDocument()
    expect(screen.getByText('buy milk')).toBeInTheDocument()
    const inbox = screen.getByRole('region', { name: 'Inbox' })
    expect(within(inbox).getByText('buy milk')).toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Todo' })).toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Done' })).toHaveTextContent('Drop notes here')
    // Checklists render as checkboxes with a progress badge.
    expect(screen.getByText('1/2')).toBeInTheDocument()
  })

  it('guides a new user to create the first page (CORE-P5)', async () => {
    vi.stubGlobal('fetch', fakeFetch([
      { path: '/api/v1/me', body: me },
      { path: '/api/v1/pages', body: { items: [] } },
      { path: '/api/v1/inbox/notes', body: { items: [], total: 0 } },
    ]))
    renderApp(<Routes><Route path="/p/:pageId" element={<BoardShell />} /></Routes>, { route: '/p/none' })
    expect(await screen.findByText('Make your first page')).toBeInTheDocument()
  })

  it('adds a note into a column with a client-generated id, and shows it at once (WEB-9)', async () => {
    const f = server()
    vi.stubGlobal('fetch', f)
    shell()
    const todo = await screen.findByRole('region', { name: 'Todo' })
    const user = userEvent.setup()
    await user.click(within(todo).getByRole('button', { name: 'Add a note' }))
    await user.type(within(todo).getByRole('textbox'), 'call dentist')
    await user.click(within(todo).getByRole('button', { name: 'Add note' }))
    expect(await within(todo).findByText('call dentist')).toBeInTheDocument() // optimistic, before the server answered
    await waitFor(() => expect(f.calls.some((c) => c.method === 'POST' && c.path === '/api/v1/notes')).toBe(true))
    const body = JSON.parse(f.calls.find((c) => c.method === 'POST' && c.path === '/api/v1/notes')!.body) as { id: string; category_id: string; parts: { text: string }[] }
    expect(body.category_id).toBe('c1')
    expect(body.parts[0]!.text).toBe('call dentist')
    expect(body.id).toMatch(/^[0-9a-f-]{36}$/)
  })

  it('dismisses with one click and offers Undo for a while (WEB-6, CORE-N8)', async () => {
    const f = server()
    vi.stubGlobal('fetch', f)
    shell()
    const inbox = await screen.findByRole('region', { name: 'Inbox' })
    const card = (await within(inbox).findByText('buy milk')).closest('article')!
    const user = userEvent.setup()
    await user.click(within(card).getByRole('button', { name: 'Dismiss' }))
    await waitFor(() => expect(within(inbox).queryByText('buy milk')).toBeNull()) // gone at once, no confirmation
    await user.click(await screen.findByRole('button', { name: 'Undo' }))
    await waitFor(() => expect(f.calls.some((c) => c.path === '/api/v1/notes/i1/restore')).toBe(true))
  })

  it('moves a note through the Move-to menu with the server doing the placement (WEB-5)', async () => {
    const f = server()
    vi.stubGlobal('fetch', f)
    shell()
    const inbox = await screen.findByRole('region', { name: 'Inbox' })
    const card = (await within(inbox).findByText('buy milk')).closest('article')!
    const user = userEvent.setup()
    await user.click(within(card).getByRole('button', { name: 'More actions' }))
    await user.click(await screen.findByRole('menuitem', { name: 'Work › Done' }))
    const done = screen.getByRole('region', { name: 'Done' })
    expect(await within(done).findByText('buy milk')).toBeInTheDocument()
    await waitFor(() => expect(f.calls.some((c) => c.path === '/api/v1/notes/i1/move')).toBe(true))
    expect(JSON.parse(f.calls.find((c) => c.path === '/api/v1/notes/i1/move')!.body)).toMatchObject({ category_id: 'c2' })
  })

  it('rolls a failed move back and says so', async () => {
    const f = server([{ method: 'POST', path: '/api/v1/notes/i1/move', status: 500, body: { code: 'internal_error' } }])
    vi.stubGlobal('fetch', f)
    shell()
    const inbox = await screen.findByRole('region', { name: 'Inbox' })
    const card = (await within(inbox).findByText('buy milk')).closest('article')!
    const user = userEvent.setup()
    await user.click(within(card).getByRole('button', { name: 'More actions' }))
    await user.click(await screen.findByRole('menuitem', { name: 'Work › Done' }))
    expect(await screen.findByText(/Nothing was changed/)).toBeInTheDocument()
    await waitFor(() => expect(within(screen.getByRole('region', { name: 'Inbox' })).getByText('buy milk')).toBeInTheDocument())
  })

  it('toggles a checklist box without entering edit mode, saving that one change (CORE-N17)', async () => {
    const f = server([{ method: 'PATCH', path: '/api/v1/notes/i2/parts/i2p', body: { note: note('i2', '- [x] eggs\n- [x] bread'), stale: false } }])
    vi.stubGlobal('fetch', f)
    shell()
    const eggs = await screen.findByRole('checkbox', { name: 'eggs' })
    await userEvent.setup().click(eggs)
    await waitFor(() => expect(f.calls.some((c) => c.method === 'PATCH')).toBe(true))
    expect(JSON.parse(f.calls.find((c) => c.method === 'PATCH')!.body)).toEqual({ text: '- [x] eggs\n- [x] bread', base_version: 1 })
  })
})
