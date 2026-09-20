import { screen, render } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fakeFetch, renderApp } from '../../test/helpers'
import { ShareDialog } from './ShareDialog'
import { SharePage, tokenFromLocation } from './SharePage'

afterEach(() => {
  vi.unstubAllGlobals()
  window.location.hash = ''
})

const shared = {
  created_at: '2026-03-01T10:00:00Z',
  expires_at: '2026-03-08T10:00:00Z',
  parts: [
    { kind: 'text', text: '- [ ] pack the bags\n<script>alert(1)</script>' },
    { kind: 'failed_attachment', failed_filename: 'lost.png' },
  ],
}

// WEB-21, CORE-SH10, SEC-SHR-7: the token comes from the fragment and goes only into a header, with no credentials.
describe('SharePage', () => {
  it('shows the not-endorsed notice (SEC-SHR-10) and reads the token from the fragment and sends it only as a header, without credentials', async () => {
    window.location.hash = '#example-fake-token-for-tests-0000'
    let seen: { token: string | null; credentials: string; url: string } | undefined
    const f = fakeFetch([
      {
        path: '/api/public/v1/share',
        handler: (req) => {
          seen = { token: req.headers.get('X-Share-Token'), credentials: req.credentials, url: req.url }
          return shared
        },
      },
    ])
    vi.stubGlobal('fetch', f)
    render(<SharePage />)
    expect(await screen.findByText('pack the bags')).toBeInTheDocument()
    expect(seen).toMatchObject({ token: 'example-fake-token-for-tests-0000', credentials: 'omit' })
    expect(seen!.url).not.toContain('example-fake')
    expect(screen.getByText(/shared by a Notekeeper user/)).toBeInTheDocument()
    // The content goes through the same safe renderer as in the app: HTML stays text, checkboxes are read-only.
    expect(document.querySelector('script')).toBeNull()
    expect(screen.getByRole('checkbox')).toBeDisabled()
    expect(screen.getByText(/lost\.png/)).toBeInTheDocument()
  })

  it('says the same thing for every failure and never asks for a login', async () => {
    window.location.hash = '#someunknowntoken0000000000000000'
    vi.stubGlobal('fetch', fakeFetch([{ path: '/api/public/v1/share', status: 404, body: { code: 'not_found' } }]))
    render(<SharePage />)
    expect(await screen.findByRole('alert')).toHaveTextContent('not available')
    expect(screen.queryByLabelText(/password/i)).toBeNull()
  })

  it('does not call the server without a token', () => {
    window.location.hash = ''
    const f = fakeFetch([])
    vi.stubGlobal('fetch', f)
    render(<SharePage />)
    expect(screen.getByRole('alert')).toBeInTheDocument()
    expect(f.calls).toHaveLength(0)
    expect(tokenFromLocation('#abc')).toBe('abc')
  })
})

const links = (over: Record<string, unknown> = {}) => ({ items: [], enabled: true, max_lifetime_seconds: 30 * 86400, ...over })

describe('ShareDialog', () => {
  it('creates a link with the chosen expiry and shows the address once, ready to copy', async () => {
    let body: unknown
    vi.stubGlobal(
      'fetch',
      fakeFetch([
        { path: '/api/v1/share-links', handler: () => links() },
        {
          method: 'POST',
          path: '/api/v1/notes/n1/share-links',
          status: 201,
          handler: async (req) => {
            body = await req.json()
            return {
              link: { id: 'l1', note_id: 'n1', excerpt: '', created_at: '2026-03-01T10:00:00Z', expires_at: '2026-03-02T10:00:00Z', view_count: 0, note_active: true },
              url: 'https://share.example.net/s#secrettoken',
            }
          },
        },
      ]),
    )
    renderApp(<ShareDialog noteId="n1" onClose={() => undefined} />)
    const user = userEvent.setup()
    await screen.findByText('No active links.')
    await user.selectOptions(screen.getByLabelText('Link works for'), '1d')
    await user.click(screen.getByRole('button', { name: 'Create link' }))
    expect(await screen.findByDisplayValue('https://share.example.net/s#secrettoken')).toBeInTheDocument()
    expect(body).toEqual({ expires_in: '1d' })
    expect(screen.getByRole('button', { name: 'Copy link' })).toBeInTheDocument()
  })

  it('offers only lifetimes within the operator maximum, and explains when sharing is off', async () => {
    vi.stubGlobal('fetch', fakeFetch([{ path: '/api/v1/share-links', handler: () => links({ max_lifetime_seconds: 2 * 86400 }) }]))
    const { unmount } = renderApp(<ShareDialog noteId="n1" onClose={() => undefined} />)
    await screen.findByText('No active links.')
    const options = Array.from(screen.getByLabelText('Link works for').querySelectorAll('option')).map((o) => o.textContent)
    expect(options).toEqual(['1 hour', '1 day'])
    unmount()

    vi.stubGlobal('fetch', fakeFetch([{ path: '/api/v1/share-links', handler: () => links({ enabled: false }) }]))
    renderApp(<ShareDialog noteId="n1" onClose={() => undefined} />)
    expect(await screen.findByText(/switched off/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Create link' })).toBeNull()
  })

  it('shows a clear message when the limit is reached', async () => {
    vi.stubGlobal(
      'fetch',
      fakeFetch([
        { path: '/api/v1/share-links', handler: () => links() },
        { method: 'POST', path: '/api/v1/notes/n1/share-links', status: 429, body: { code: 'share_limit' } },
      ]),
    )
    renderApp(<ShareDialog noteId="n1" onClose={() => undefined} />)
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Create link' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('too many links')
  })
})
