import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from './client'
import { cancelScheduledRefresh } from './session'
import { fakeFetch, me } from '../test/helpers'

afterEach(() => {
  cancelScheduledRefresh()
  vi.unstubAllGlobals()
})

describe('api client', () => {
  it('sends the CSRF client header on every call', async () => {
    const seen: string[] = []
    vi.stubGlobal(
      'fetch',
      fakeFetch([{ path: '/api/v1/me', handler: (r) => (seen.push(r.headers.get('X-Notekeeper-Client') ?? ''), me) }]),
    )
    await api.GET('/api/v1/me')
    expect(seen).toEqual(['web'])
  })

  it('renews the session once on a 401 and retries the call', async () => {
    let attempts = 0
    const f = fakeFetch([
      {
        path: '/api/v1/me',
        handler: () => (attempts++ === 0 ? new Response(JSON.stringify({ code: 'unauthenticated' }), { status: 401 }) : me),
      },
      { method: 'POST', path: '/api/v1/auth/refresh', body: {} },
    ])
    vi.stubGlobal('fetch', f)
    const res = await api.GET('/api/v1/me')
    expect(res.data?.username).toBe('alice')
    expect(f.calls.map((c) => c.path)).toEqual(['/api/v1/me', '/api/v1/auth/refresh', '/api/v1/me'])
  })

  it('does not try to renew when the auth endpoints themselves answer 401', async () => {
    const f = fakeFetch([{ method: 'POST', path: '/api/v1/auth/login', status: 401, body: { code: 'invalid_credentials' } }])
    vi.stubGlobal('fetch', f)
    const res = await api.POST('/api/v1/auth/login', { body: { username: 'a', password: 'b', remember: true, client_kind: 'web' } })
    expect(res.response.status).toBe(401)
    expect(f.calls).toHaveLength(1)
  })

  it('gives up after one renewal: a second 401 is returned to the caller', async () => {
    const f = fakeFetch([
      { path: '/api/v1/me', status: 401, body: { code: 'unauthenticated' } },
      { method: 'POST', path: '/api/v1/auth/refresh', body: {} },
    ])
    vi.stubGlobal('fetch', f)
    const res = await api.GET('/api/v1/me')
    expect(res.response.status).toBe(401)
    expect(f.calls.filter((c) => c.path === '/api/v1/me')).toHaveLength(2)
  })
})
