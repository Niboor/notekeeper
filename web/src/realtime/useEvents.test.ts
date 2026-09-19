import { QueryClient } from '@tanstack/react-query'
import { describe, expect, it, vi } from 'vitest'
import { applyChange } from './useEvents'

describe('applyChange', () => {
  it('refreshes the views a note change can affect, and only those', () => {
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    applyChange(qc, { entity_type: 'note', entity_id: 'n1', op: 'upsert' })
    const keys = spy.mock.calls.map((c) => JSON.stringify((c[0] as { queryKey: unknown }).queryKey))
    expect(keys).toContain('["inbox"]')
    expect(keys).toContain('["note","n1"]')
    expect(keys).not.toContain('["identities"]')
  })
  it('refreshes chat links on identity changes', () => {
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    applyChange(qc, { entity_type: 'identity', entity_id: 'i1', op: 'delete' })
    expect(spy).toHaveBeenCalledWith({ queryKey: ['identities'] })
  })
  it('refreshes everything for an entity it does not know', () => {
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    applyChange(qc, { entity_type: 'future-thing', entity_id: 'x', op: 'upsert' })
    expect(spy).toHaveBeenCalledWith()
  })
})
