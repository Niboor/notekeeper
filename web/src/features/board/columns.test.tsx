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

// WEB-22, CORE-P2, CORE-P3, WEB-5: the column menu moves a column one step or to another page, at once on
// screen, and puts it back when the server refuses.

const columns = ['c1', 'c2', 'c3']
const names: Record<string, string> = { c1: 'Todo', c2: 'Doing', c3: 'Done' }

/** A small server with one page of three columns and an empty second page; the moves are recorded. */
function server(opts: { failMoves?: boolean } = {}) {
  const order = { p1: [...columns], p2: [] as string[] }
  const page = (id: 'p1' | 'p2') => ({ id, name: id === 'p1' ? 'Work' : 'Home', version: 1, categories: order[id].map((c) => ({ id: c, name: names[c] })) })
  const patches: Record<string, unknown>[] = []
  const f = fakeFetch([
    { path: '/api/v1/me', body: me },
    { path: '/api/v1/pages', handler: () => ({ items: [page('p1'), page('p2')] }) },
    ...(['p1', 'p2'] as const).map((id) => ({
      path: `/api/v1/pages/${id}/board`,
      handler: () => ({
        page: { id, name: page(id).name, version: 1 }, inbox_total: 0,
        categories: order[id].map((c) => ({ category: { id: c, page_id: id, name: names[c], version: 1 }, notes: [], total: 0 })),
      }),
    })),
    { path: '/api/v1/inbox/notes', body: { items: [], total: 0 } },
    {
      method: 'PATCH',
      path: /\/api\/v1\/categories\/[^/]+$/,
      handler: async (r) => {
        const id = new URL(r.url).pathname.split('/')[4]!
        const body = (await r.clone().json()) as { page_id?: string; after_id: string | null; before_id: string | null }
        patches.push({ id, ...body })
        if (opts.failMoves) return new Response(JSON.stringify({ code: 'internal' }), { status: 500 })
        const from = (Object.keys(order) as (keyof typeof order)[]).find((p) => order[p].includes(id))!
        const to = (body.page_id ?? from) as keyof typeof order
        order[from] = order[from].filter((c) => c !== id)
        const at = body.after_id ? order[to].indexOf(body.after_id) + 1 : body.before_id ? order[to].indexOf(body.before_id) : order[to].length
        order[to].splice(at, 0, id)
        return { id, page_id: to, name: names[id], version: 2 }
      },
    },
  ])
  return Object.assign(f, { patches })
}

const shell = () => renderApp(<Routes><Route path="/p/:pageId" element={<BoardShell />} /></Routes>, { route: '/p/p1' })
const shown = () => screen.getAllByRole('region').map((r) => r.getAttribute('aria-label')).filter((n) => n && n !== 'Inbox')
const menu = async (name: string) => {
  await userEvent.click(await screen.findByRole('button', { name: new RegExp(`^${name}: Page options`) }))
  return screen.getByRole('menu')
}

describe('moving columns through the menu', () => {
  it('offers only the moves that are possible', async () => {
    vi.stubGlobal('fetch', server())
    shell()
    let m = await menu('Todo')
    expect(within(m).queryByRole('menuitem', { name: 'Move left' })).toBeNull()
    expect(within(m).getByRole('menuitem', { name: 'Move right' })).toBeInTheDocument()
    expect(within(m).getByRole('menuitem', { name: 'Home' })).toBeInTheDocument() // the other page, not the current one
    expect(within(m).queryByRole('menuitem', { name: 'Work' })).toBeNull()
    await userEvent.keyboard('{Escape}')
    m = await menu('Done')
    expect(within(m).getByRole('menuitem', { name: 'Move left' })).toBeInTheDocument()
    expect(within(m).queryByRole('menuitem', { name: 'Move right' })).toBeNull()
  })

  it('moves a column one step, at once, and tells the server where', async () => {
    const f = server()
    vi.stubGlobal('fetch', f)
    shell()
    await screen.findByRole('region', { name: 'Todo' })
    expect(shown()).toEqual(['Todo', 'Doing', 'Done'])
    await userEvent.click(within(await menu('Todo')).getByRole('menuitem', { name: 'Move right' }))
    await waitFor(() => expect(shown()).toEqual(['Doing', 'Todo', 'Done']))
    await waitFor(() => expect(f.patches).toEqual([{ id: 'c1', after_id: 'c2', before_id: 'c3' }]))
    expect(shown()).toEqual(['Doing', 'Todo', 'Done']) // and still, once the server's answer is in
  })

  it('moves a column to another page, which takes it off this one, and Undo brings it back', async () => {
    const f = server()
    vi.stubGlobal('fetch', f)
    shell()
    await screen.findByRole('region', { name: 'Doing' })
    await userEvent.click(within(await menu('Doing')).getByRole('menuitem', { name: 'Home' }))
    await waitFor(() => expect(shown()).toEqual(['Todo', 'Done']))
    expect(f.patches[0]).toEqual({ id: 'c2', page_id: 'p2', after_id: null, before_id: null })
    await userEvent.click(await screen.findByRole('button', { name: 'Undo' }))
    await waitFor(() => expect(shown()).toEqual(['Todo', 'Doing', 'Done']))
    expect(f.patches[1]).toEqual({ id: 'c2', page_id: 'p1', after_id: 'c1', before_id: 'c3' })
  })

  it('puts a column back and says so when the server refuses', async () => {
    const f = server({ failMoves: true })
    vi.stubGlobal('fetch', f)
    shell()
    await screen.findByRole('region', { name: 'Todo' })
    await userEvent.click(within(await menu('Todo')).getByRole('menuitem', { name: 'Move right' }))
    expect(await screen.findByText('That did not work. Nothing was changed.')).toBeInTheDocument()
    await waitFor(() => expect(shown()).toEqual(['Todo', 'Doing', 'Done']))
  })
})
