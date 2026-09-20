import { QueryClient } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { applyChange } from './useEvents'

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
