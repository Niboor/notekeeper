import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cancelScheduledRefresh, onSessionLost, refreshSession, scheduleRefresh } from './session'
import { fakeFetch } from '../test/helpers'

describe('session renewal', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => {
    cancelScheduledRefresh()
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('shares one request between concurrent refreshes in a tab (single flight)', async () => {
    const f = fakeFetch([{ method: 'POST', path: '/api/v1/auth/refresh', body: { expires_at: new Date(Date.now() + 900_000).toISOString() } }])
    vi.stubGlobal('fetch', f)
    const results = await Promise.all([refreshSession(), refreshSession(), refreshSession()])
    expect(results).toEqual([true, true, true])
    expect(f.calls.filter((c) => c.path === '/api/v1/auth/refresh')).toHaveLength(1)
  })

  it('reports a lost session only when the server refuses the refresh token', async () => {
    const lost = vi.fn()
    const off = onSessionLost(lost)
    vi.stubGlobal('fetch', fakeFetch([{ method: 'POST', path: '/api/v1/auth/refresh', status: 401, body: { code: 'session_expired' } }]))
    expect(await refreshSession()).toBe(false)
    expect(lost).toHaveBeenCalledOnce()

    lost.mockClear()
    vi.stubGlobal('fetch', fakeFetch([{ method: 'POST', path: '/api/v1/auth/refresh', status: 503 }]))
    expect(await refreshSession()).toBe(true) // server trouble is not a logout
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('offline')))
    expect(await refreshSession()).toBe(true) // neither is being offline
    expect(lost).not.toHaveBeenCalled()
    off()
  })

  it('renews proactively at about 80% of the access token lifetime', async () => {
    const f = fakeFetch([{ method: 'POST', path: '/api/v1/auth/refresh', body: {} }])
    vi.stubGlobal('fetch', f)
    scheduleRefresh(new Date(Date.now() + 900_000))
    await vi.advanceTimersByTimeAsync(719_000)
    expect(f.calls).toHaveLength(0)
    await vi.advanceTimersByTimeAsync(2_000)
    expect(f.calls).toHaveLength(1)
  })

  it('does not schedule for an already expired token', () => {
    const f = fakeFetch([])
    vi.stubGlobal('fetch', f)
    scheduleRefresh(new Date(Date.now() - 1000))
    vi.advanceTimersByTime(60_000)
    expect(f.calls).toHaveLength(0)
  })
})
