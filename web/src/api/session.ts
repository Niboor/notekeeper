// Session renewal (docs/design/03-auth.md section 2.3, docs/design/07-web-app.md section 3).
// The access cookie lives 15 minutes; the refresh cookie rotates on every use. Refreshes are
// single-flight within a tab and serialised across tabs with the Web Locks API; the server's
// 60-second grace window is the backstop for races.

export const CLIENT_HEADER = { 'X-Notekeeper-Client': 'web' } as const

type Listener = () => void
const lostListeners = new Set<Listener>()

/** Registers a callback for when the session is really gone (refresh was refused). */
export function onSessionLost(cb: Listener): () => void {
  lostListeners.add(cb)
  return () => lostListeners.delete(cb)
}

let inflight: Promise<boolean> | null = null
let timer: ReturnType<typeof setTimeout> | undefined

async function doRefresh(): Promise<boolean> {
  try {
    const res = await fetch('/api/v1/auth/refresh', { method: 'POST', headers: CLIENT_HEADER })
    if (res.status === 401) return false
    if (!res.ok) return true // transient server trouble: keep the session, retry later
    const body = (await res.json()) as { expires_at?: string }
    if (body.expires_at) scheduleRefresh(new Date(body.expires_at))
    return true
  } catch {
    return true // offline: do not throw the user out
  }
}

/** Renews the session. Returns false only when the server says the session is over. */
export function refreshSession(): Promise<boolean> {
  inflight ??= (async () => {
    const ok =
      typeof navigator !== 'undefined' && navigator.locks
        ? await navigator.locks.request('nk-refresh', doRefresh)
        : await doRefresh()
    if (!ok) lostListeners.forEach((cb) => cb())
    return ok
  })().finally(() => {
    inflight = null
  })
  return inflight
}

/** Schedules a proactive renewal at about 80% of the access token's remaining lifetime. */
export function scheduleRefresh(expiresAt: Date, now: number = Date.now()): void {
  clearTimeout(timer)
  const remaining = expiresAt.getTime() - now
  if (remaining <= 0) return
  timer = setTimeout(() => void refreshSession(), Math.max(remaining * 0.8, 1000))
}

export function cancelScheduledRefresh(): void {
  clearTimeout(timer)
}
