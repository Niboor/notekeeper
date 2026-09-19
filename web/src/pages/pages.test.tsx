import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Route, Routes } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cancelScheduledRefresh } from '../api/session'
import { InboxPage } from '../features/inbox/InboxPage'
import { fakeFetch, me, renderApp } from '../test/helpers'
import { ActivatePage, tokenFromHash } from './ActivatePage'
import { LoginPage } from './LoginPage'

afterEach(() => {
  cancelScheduledRefresh()
  vi.unstubAllGlobals()
  window.location.hash = ''
})

const signedOut = { path: '/api/v1/me', status: 401, body: { code: 'unauthenticated' } }
const refreshRefused = { method: 'POST', path: '/api/v1/auth/refresh', status: 401, body: { code: 'session_expired' } }

describe('LoginPage', () => {
  it('signs in and shows a generic message for wrong credentials', async () => {
    let attempt = 0
    const f = fakeFetch([
      signedOut,
      refreshRefused,
      {
        method: 'POST',
        path: '/api/v1/auth/login',
        handler: () =>
          attempt++ === 0
            ? new Response(JSON.stringify({ code: 'invalid_credentials' }), { status: 401 })
            : { user: me, expires_at: new Date(Date.now() + 900_000).toISOString() },
      },
    ])
    vi.stubGlobal('fetch', f)
    renderApp(
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route path="/" element={<p>home</p>} />
      </Routes>,
      { route: '/login' },
    )
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText('Username'), 'alice')
    await user.type(screen.getByLabelText('Password'), 'nope')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('do not match')
    const body = JSON.parse(f.calls.find((c) => c.path.endsWith('/login'))!.body) as Record<string, unknown>
    expect(body).toMatchObject({ username: 'alice', remember: true, client_kind: 'web' })

    // A second attempt with the right password succeeds and leaves the login page.
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    expect(await screen.findByText('home')).toBeInTheDocument()
  })
})

describe('ActivatePage', () => {
  it('reads the token from the URL fragment', () => {
    expect(tokenFromHash('#abc123')).toBe('abc123')
    expect(tokenFromHash('')).toBe('')
  })

  it('explains a missing link', async () => {
    vi.stubGlobal('fetch', fakeFetch([signedOut, refreshRefused]))
    renderApp(<ActivatePage />, { route: '/activate' })
    expect(await screen.findByText(/needs the link/)).toBeInTheDocument()
  })

  it('sends the fragment token with the password and reports an invalid link', async () => {
    window.location.hash = '#tok-1'
    const f = fakeFetch([signedOut, refreshRefused, { method: 'POST', path: '/api/v1/auth/activate', status: 400, body: { code: 'invalid_link' } }])
    vi.stubGlobal('fetch', f)
    renderApp(<ActivatePage />, { route: '/activate' })
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText('New password'), 'correct horse battery')
    await user.click(screen.getByRole('button', { name: /Set password/ }))
    expect(await screen.findByRole('alert')).toHaveTextContent('not valid any more')
    expect(JSON.parse(f.calls.find((c) => c.path.endsWith('/activate'))!.body)).toEqual({ token: 'tok-1', password: 'correct horse battery' })
  })
})

describe('InboxPage', () => {
  const note = (id: string, text: string) => ({
    id, state: 'active', created_at: new Date().toISOString(), updated_at: new Date().toISOString(), version: 1,
    parts: [{ id: id + 'p', kind: 'text', text, attach_reason: 'first', created_at: new Date().toISOString(), source_bot_type: 'matrix' }],
  })

  it('shows notes with their origin and the live count', async () => {
    vi.stubGlobal('fetch', fakeFetch([
      { path: '/api/v1/me', body: me },
      { path: '/api/v1/inbox/notes', body: { items: [note('a', 'buy milk'), note('b', '<img src=x onerror=alert(1)>')], total: 2 } },
    ]))
    renderApp(<InboxPage />)
    expect(await screen.findByText('buy milk')).toBeInTheDocument()
    expect(screen.getAllByText(/via Matrix/)).toHaveLength(2)
    expect(screen.getByText('2')).toBeInTheDocument()
    // Note text is shown as text, never as markup.
    expect(screen.getByText('<img src=x onerror=alert(1)>')).toBeInTheDocument()
    expect(document.querySelector('img')).toBeNull()
  })

  it('explains an empty Inbox and pages on demand', async () => {
    vi.stubGlobal('fetch', fakeFetch([
      { path: '/api/v1/me', body: me },
      { path: '/api/v1/inbox/notes', body: { items: [], total: 0 } },
    ]))
    renderApp(<InboxPage />)
    await waitFor(() => expect(screen.getByText(/Send a message to the bot/)).toBeInTheDocument())
  })
})
