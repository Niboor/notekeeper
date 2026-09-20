import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { renderHook } from '@testing-library/react'
import { createElement, type ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cancelScheduledRefresh } from '../api/session'
import { fakeFetch } from '../test/helpers'
import { applyChange, useEvents } from './useEvents'

describe('applyChange', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it('refreshes the views a note change can affect, and only those', () => {
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    applyChange(qc, { entity_type: 'note', entity_id: 'n1', op: 'upsert' })
    vi.advanceTimersByTime(300)
    const keys = spy.mock.calls.map((c) => JSON.stringify((c[0] as { queryKey: unknown }).queryKey))
    expect(keys).toContain('["inbox"]')
    expect(keys).toContain('["note","n1"]')
    expect(keys).not.toContain('["identities"]')
  })
  it('refreshes chat links on identity changes', () => {
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    applyChange(qc, { entity_type: 'identity', entity_id: 'i1', op: 'delete' })
    vi.advanceTimersByTime(300)
    expect(spy).toHaveBeenCalledWith({ queryKey: ['identities'] })
  })
  it('refreshes everything for an entity it does not know', () => {
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    applyChange(qc, { entity_type: 'future-thing', entity_id: 'x', op: 'upsert' })
    vi.advanceTimersByTime(300)
    expect(spy).toHaveBeenCalledWith()
  })
  it('turns a burst of changes into one refetch per view (CR-052)', () => {
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    for (let i = 0; i < 40; i++) applyChange(qc, { entity_type: 'note', entity_id: `n${i % 4}`, op: 'upsert' })
    expect(spy).not.toHaveBeenCalled()
    vi.advanceTimersByTime(300)
    const keys = spy.mock.calls.map((c) => JSON.stringify((c[0] as { queryKey: unknown }).queryKey))
    expect(keys.filter((k) => k === '["inbox"]')).toHaveLength(1)
    expect(keys.filter((k) => k === '["board"]')).toHaveLength(1)
    expect(keys.filter((k) => k.startsWith('["note"'))).toHaveLength(4)
  })
})

// A stream that dies is not a reason to rotate the refresh token: the session is checked first, and renewed
// only when it has lapsed (CR-054). Safari, for one, keeps the streams of closed pages open for a while, which
// makes the server refuse new ones.
describe('useEvents reconnecting', () => {
  class FakeSource {
    static CLOSED = 2
    static instances: FakeSource[] = []
    readyState = 0
    onerror: (() => void) | null = null
    constructor(public url: string) {
      FakeSource.instances.push(this)
    }
    addEventListener() {}
    close() {
      this.readyState = FakeSource.CLOSED
    }
    /** What the browser does when the server refuses the stream. */
    refused() {
      this.readyState = FakeSource.CLOSED
      this.onerror?.()
    }
  }

  const problem = (status: number) => new Response(JSON.stringify({ code: 'unauthenticated' }), { status, headers: { 'Content-Type': 'application/json' } })
  const me = { id: 'u1', username: 'alice', display_name: 'Alice', is_admin: false, timezone: 'UTC', settings: {} }

  beforeEach(() => {
    vi.useFakeTimers()
    FakeSource.instances = []
    vi.stubGlobal('EventSource', FakeSource)
    vi.spyOn(Math, 'random').mockReturnValue(0.5) // a wait of exactly one second after the first failure
  })
  afterEach(() => {
    cancelScheduledRefresh()
    vi.useRealTimers()
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  const mount = () => {
    const client = new QueryClient()
    const wrapper = ({ children }: { children: ReactNode }) => createElement(QueryClientProvider, { client }, children)
    return renderHook(() => useEvents(true), { wrapper })
  }

  it('reopens the stream without touching the refresh token when the session is fine', async () => {
    const fetchMock = fakeFetch([{ path: '/api/v1/me', body: me }, { method: 'POST', path: '/api/v1/auth/refresh', body: {} }])
    vi.stubGlobal('fetch', fetchMock)
    const view = mount()
    expect(FakeSource.instances).toHaveLength(1)
    FakeSource.instances[0]!.refused() // for example: too many streams
    await vi.advanceTimersByTimeAsync(1500)
    expect(FakeSource.instances).toHaveLength(2)
    expect(fetchMock.calls.filter((c) => c.path === '/api/v1/auth/refresh')).toHaveLength(0)
    view.unmount()
  })

  it('renews the session once when it has lapsed, then reopens the stream', async () => {
    let meCalls = 0
    const fetchMock = fakeFetch([
      { path: '/api/v1/me', handler: () => (++meCalls === 1 ? problem(401) : me) },
      { method: 'POST', path: '/api/v1/auth/refresh', body: { expires_at: new Date(Date.now() + 15 * 60_000).toISOString() } },
    ])
    vi.stubGlobal('fetch', fetchMock)
    const view = mount()
    FakeSource.instances[0]!.refused()
    await vi.advanceTimersByTimeAsync(1500)
    expect(fetchMock.calls.filter((c) => c.path === '/api/v1/auth/refresh')).toHaveLength(1)
    expect(FakeSource.instances).toHaveLength(2)
    view.unmount()
  })

  it('stays closed when the session is over', async () => {
    const fetchMock = fakeFetch([
      { path: '/api/v1/me', handler: () => problem(401) },
      { method: 'POST', path: '/api/v1/auth/refresh', handler: () => problem(401) },
    ])
    vi.stubGlobal('fetch', fetchMock)
    const view = mount()
    FakeSource.instances[0]!.refused()
    await vi.advanceTimersByTimeAsync(5000)
    expect(FakeSource.instances).toHaveLength(1)
    view.unmount()
  })

  it('tries again after a network error', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('offline') }))
    const view = mount()
    FakeSource.instances[0]!.refused()
    await vi.advanceTimersByTimeAsync(1500)
    expect(FakeSource.instances).toHaveLength(2)
    view.unmount()
  })
})
