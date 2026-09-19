import { useQueryClient, type QueryClient } from '@tanstack/react-query'
import { useEffect } from 'react'
import { refreshSession } from '../api/session'

interface ChangeEvent {
  entity_type: string
  entity_id: string
  op: 'upsert' | 'delete'
}

/** Maps a change notification to the cached views that may now be stale (docs/design/07 section 4). */
export function applyChange(qc: QueryClient, c: ChangeEvent): void {
  switch (c.entity_type) {
    case 'note':
      void qc.invalidateQueries({ queryKey: ['inbox'] })
      void qc.invalidateQueries({ queryKey: ['board'] })
      void qc.invalidateQueries({ queryKey: ['trash'] })
      void qc.invalidateQueries({ queryKey: ['note', c.entity_id] })
      break
    case 'identity':
      void qc.invalidateQueries({ queryKey: ['identities'] })
      break
    case 'session':
      void qc.invalidateQueries({ queryKey: ['sessions'] })
      break
    default:
      void qc.invalidateQueries()
  }
}

/**
 * Keeps the cache live through one EventSource per tab (WEB-11). The browser reconnects on its
 * own and sends Last-Event-ID, so nothing is missed; "hello" (a fresh connection) and "resync"
 * refetch everything. The server ends each stream when its access token lapses, after which the
 * browser reconnects with the renewed cookie; if the stream is refused outright we renew and retry.
 */
export function useEvents(enabled: boolean): void {
  const qc = useQueryClient()
  useEffect(() => {
    if (!enabled) return
    let source: EventSource | undefined
    let closed = false
    let retry: ReturnType<typeof setTimeout> | undefined

    const open = () => {
      source = new EventSource('/api/v1/events')
      source.addEventListener('hello', () => void qc.invalidateQueries())
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
        // CLOSED means the browser gave up (for instance a 401): renew the session, then reopen.
        if (source?.readyState === EventSource.CLOSED && !closed) {
          void refreshSession().then((ok) => {
            if (ok && !closed) retry = setTimeout(open, 1000)
          })
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
