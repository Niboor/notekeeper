import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fakeFetch, me, renderApp } from '../../test/helpers'
import { Composer } from './Composer'

afterEach(() => vi.unstubAllGlobals())

const created = {
  id: 'n1', state: 'active', category_id: null, created_at: '2026-03-01T10:00:00Z', updated_at: '2026-03-01T10:00:00Z', version: 1,
  parts: [{ id: 'p1', kind: 'text', text: 'buy oat milk', attach_reason: 'first', created_at: '2026-03-01T10:00:00Z' }],
}

// CR-050 (WEB-9): a note that could not be saved is not thrown away with the composer.
describe('Composer', () => {
  it('stays open with the text when saving fails, and adds the note with the same id when it is tried again', async () => {
    let attempt = 0
    const ids: string[] = []
    const f = fakeFetch([
      { path: '/api/v1/me', body: me },
      {
        method: 'POST',
        path: '/api/v1/notes',
        handler: async (req) => {
          ids.push(((await req.json()) as { id: string }).id)
          attempt++
          return attempt === 1 ? new Response('{"code":"internal_error"}', { status: 500 }) : new Response(JSON.stringify(created), { status: 201 })
        },
      },
    ])
    vi.stubGlobal('fetch', f)
    const onClose = vi.fn()
    renderApp(<Composer categoryId={null} onClose={onClose} />)
    const user = userEvent.setup()
    await user.type(screen.getByRole('textbox'), 'buy oat milk')
    await user.click(screen.getByRole('button', { name: /add note/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/not saved/i)
    expect(onClose).not.toHaveBeenCalled()
    expect(screen.getByRole('textbox')).toHaveValue('buy oat milk')

    await user.click(screen.getByRole('button', { name: /add note/i }))
    await vi.waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
    expect(ids).toHaveLength(2)
    expect(ids[0]).toBe(ids[1]) // one note, however often adding is tried
  })
})
