import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Route, Routes } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cancelScheduledRefresh } from '../../api/session'
import { TopbarSlot } from '../../components/topbar'
import { fakeFetch, me, renderApp } from '../../test/helpers'
import { BoardShell } from './BoardShell'

afterEach(() => {
  cancelScheduledRefresh()
  vi.unstubAllGlobals()
})

// WEB-23, CORE-P1, WEB-5: the page menu moves a page among the tabs, at once on screen, and puts it back when
// the server refuses.

const names: Record<string, string> = { p1: 'Work', p2: 'Home', p3: 'Play' }

function server(opts: { fail?: boolean } = {}) {
  let order = ['p1', 'p2', 'p3']
  const patches: Record<string, unknown>[] = []
  const f = fakeFetch([
    { path: '/api/v1/me', body: me },
    { path: '/api/v1/pages', handler: () => ({ items: order.map((id) => ({ id, name: names[id], version: 1, categories: [] })) }) },
    { path: /\/api\/v1\/pages\/[^/]+\/board$/, handler: (r) => ({ page: { id: new URL(r.url).pathname.split('/')[4], name: 'x', version: 1 }, inbox_total: 0, categories: [] }) },
    { path: '/api/v1/inbox/notes', body: { items: [], total: 0 } },
    {
      method: 'PATCH',
      path: /\/api\/v1\/pages\/[^/]+$/,
      handler: async (r) => {
        const id = new URL(r.url).pathname.split('/')[4]!
        const body = (await r.clone().json()) as { after_id: string | null; before_id: string | null }
        patches.push({ id, ...body })
        if (opts.fail) return new Response(JSON.stringify({ code: 'internal' }), { status: 500 })
        const rest = order.filter((p) => p !== id)
        rest.splice(body.after_id ? rest.indexOf(body.after_id) + 1 : body.before_id ? rest.indexOf(body.before_id) : rest.length, 0, id)
        order = rest
        return { id, name: names[id], version: 2 }
      },
    },
  ])
  return Object.assign(f, { patches })
}

/** The board puts its page tabs into the top bar's slot, which the app's layout provides. */
const shell = (page: string) => {
  const slot = document.body.appendChild(document.createElement('div'))
  return renderApp(
    <TopbarSlot value={slot}>
      <Routes>
        <Route path="/p/:pageId" element={<BoardShell />} />
      </Routes>
    </TopbarSlot>,
    { route: `/p/${page}` },
  )
}
const tabs = () => within(screen.getByRole('navigation', { name: 'Pages' })).getAllByRole('link').map((a) => a.textContent)
const menu = async () => {
  await userEvent.click(await screen.findByRole('button', { name: 'Page options' }))
  return screen.getByRole('menu')
}

// WEB-23, WEB-5: moving pages through the menu.
describe('moving pages through the menu', () => {
  it('offers only the moves that are possible', async () => {
    vi.stubGlobal('fetch', server())
    shell('p1')
    const m = await menu()
    expect(within(m).queryByRole('menuitem', { name: 'Move left' })).toBeNull()
    expect(within(m).getByRole('menuitem', { name: 'Move right' })).toBeInTheDocument()
  })

  it('moves the page one step, at once, and tells the server between which pages', async () => {
    const f = server()
    vi.stubGlobal('fetch', f)
    shell('p1')
    await waitFor(() => expect(tabs()).toEqual(['Work', 'Home', 'Play']))
    await userEvent.click(within(await menu()).getByRole('menuitem', { name: 'Move right' }))
    await waitFor(() => expect(tabs()).toEqual(['Home', 'Work', 'Play']))
    await waitFor(() => expect(f.patches).toEqual([{ id: 'p1', after_id: 'p2', before_id: 'p3' }]))
    expect(tabs()).toEqual(['Home', 'Work', 'Play']) // and still, once the server's answer is in
  })

  it('moves the last page to the front', async () => {
    const f = server()
    vi.stubGlobal('fetch', f)
    shell('p3')
    await waitFor(() => expect(tabs()).toEqual(['Work', 'Home', 'Play']))
    const m = await menu()
    expect(within(m).queryByRole('menuitem', { name: 'Move right' })).toBeNull()
    await userEvent.click(within(m).getByRole('menuitem', { name: 'Move left' }))
    await waitFor(() => expect(f.patches).toEqual([{ id: 'p3', after_id: 'p1', before_id: 'p2' }]))
    await waitFor(() => expect(tabs()).toEqual(['Work', 'Play', 'Home']))
  })

  it('puts the page back and says so when the server refuses', async () => {
    vi.stubGlobal('fetch', server({ fail: true }))
    shell('p1')
    await waitFor(() => expect(tabs()).toEqual(['Work', 'Home', 'Play']))
    await userEvent.click(within(await menu()).getByRole('menuitem', { name: 'Move right' }))
    expect(await screen.findByText('That did not work. Nothing was changed.')).toBeInTheDocument()
    await waitFor(() => expect(tabs()).toEqual(['Work', 'Home', 'Play']))
  })
})
