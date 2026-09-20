import { useQueryClient, type QueryClient } from '@tanstack/react-query'
import { useEffect } from 'react'
import { api } from '../api/client'

interface ChangeEvent {
  entity_type: string
  entity_id: string
  op: 'upsert' | 'delete'
}

const KEYS_BY_ENTITY: Record<string, string[]> = {
  note: ['inbox', 'board', 'trash'],
  reminder: ['reminders', 'inbox', 'board'],
  notification: ['notifications'],
  share_link: ['share-links'],
  identity: ['identities'],
  session: ['sessions'],
}

// A burst of changes (a merge, deleting a column, a rebalance) is one refetch, not one per change: the
// invalidations of the next quarter second are collected and run once (CR-052).
const DEBOUNCE_MS = 250
const pending = new WeakMap<QueryClient, { keys: Set<string>; all: boolean; timer: ReturnType<typeof setTimeout> }>()

function scheduleInvalidate(qc: QueryClient, keys: string[] | 'all', noteId?: string): void {
  let p = pending.get(qc)
  if (!p) {
    const fresh = { keys: new Set<string>(), all: false, timer: setTimeout(() => flush(qc), DEBOUNCE_MS) }
    pending.set(qc, fresh)
    p = fresh
  }
  if (keys === 'all') p.all = true
  else keys.forEach((k) => p.keys.add(k))
  if (noteId) p.keys.add(`note:${noteId}`)
}

function flush(qc: QueryClient): void {
  const p = pending.get(qc)
  pending.delete(qc)
  if (!p) return
  if (p.all) {
    void qc.invalidateQueries()
    return
  }
  for (const k of p.keys) {
    if (k.startsWith('note:')) void qc.invalidateQueries({ queryKey: ['note', k.slice(5)] })
    else void qc.invalidateQueries({ queryKey: [k] })
  }
}

/** Maps a change notification to the cached views that may now be stale (docs/design/07 section 4). */
export function applyChange(qc: QueryClient, c: ChangeEvent): void {
  const keys = KEYS_BY_ENTITY[c.entity_type]
  if (!keys) scheduleInvalidate(qc, 'all')
  else scheduleInvalidate(qc, keys, c.entity_type === 'note' ? c.entity_id : undefined)
}

/**
 * Keeps the cache live through one EventSource per tab (WEB-11). The browser reconnects on its
 * own and sends Last-Event-ID, so nothing is missed; "hello" (a fresh connection) and "resync"
 * refetch everything. The server ends each stream when its access token lapses, after which the
 * browser reconnects with the renewed cookie; if the stream is refused outright we check the session (renewing it when it lapsed) and retry.
 */
export function useEvents(enabled: boolean): void {
  const qc = useQueryClient()
  useEffect(() => {
    if (!enabled) return
    let source: EventSource | undefined
    let closed = false
    let retry: ReturnType<typeof setTimeout> | undefined
    let failures = 0

    const open = () => {
      source = new EventSource('/api/v1/events')
      source.addEventListener('hello', () => {
        failures = 0
        void qc.invalidateQueries()
      })
      source.addEventListener('resync', () => void qc.invalidateQueries())
      source.addEventListener('change', (e) => {
        try {
          applyChange(qc, JSON.parse((e as MessageEvent<string>).data) as ChangeEvent)
        } catch {
          void qc.invalidateQueries()
        }
      })
      source.addEventListener('reconnect', () => {
        source?.close()
        retry = setTimeout(open, 500)
      })
      source.onerror = () => {
        // CLOSED means the browser gave up: a 401, too many streams (another tab, or a browser that keeps the
        // streams of closed pages for a while), a proxy error during a deploy. Only the first is a matter of the
        // session, and asking for /me finds that out and renews the session when it is needed. Renewing it
        // straight away would rotate the refresh token on every refusal, and a token that other tabs still
        // hold then looks stolen (CR-054). Waiting longer after every failure keeps a server that keeps
        // refusing from being hammered.
        if (source?.readyState === EventSource.CLOSED && !closed) {
          const wait = Math.min(30000, 1000 * 2 ** failures) * (0.75 + Math.random() * 0.5)
          failures++
          const again = () => {
            if (!closed) retry = setTimeout(open, wait)
          }
          api.GET('/api/v1/me').then(({ response }) => response.status !== 401 && again(), again) // a network error: try again later too
        }
      }
    }
    open()
    return () => {
      closed = true
      clearTimeout(retry)
      source?.close()
    }
  }, [enabled, qc])
}
