import { useQueryClient } from '@tanstack/react-query'
import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import { api, ApiError, unwrap, unwrapEmpty, type Schemas } from '../api/client'
import { cancelScheduledRefresh, onSessionLost, scheduleRefresh } from '../api/session'

type Me = Schemas['Me']

interface AuthState {
  /** undefined while the first check runs, null when signed out. */
  user: Me | null | undefined
  login(username: string, password: string, remember: boolean): Promise<void>
  activate(token: string, password: string): Promise<void>
  logout(): Promise<void>
}

const AuthContext = createContext<AuthState | null>(null)

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth outside AuthProvider')
  return ctx
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient()
  const [user, setUser] = useState<Me | null | undefined>(undefined)

  const signedOut = useCallback(() => {
    cancelScheduledRefresh()
    queryClient.clear() // nothing of the previous user may stay in memory
    setUser(null)
  }, [queryClient])

  // First load: the cookies decide whether we are signed in. A 401 is renewed silently by the fetch wrapper.
  useEffect(() => {
    let cancelled = false
    void api.GET('/api/v1/me').then((res) => {
      if (cancelled) return
      if (res.data) {
        setUser(res.data)
        // Renew before the first access token lapses; later renewals reschedule themselves.
        scheduleRefresh(new Date(Date.now() + 15 * 60_000))
      } else {
        setUser(null)
      }
    })
    return () => {
      cancelled = true
    }
  }, [])

  useEffect(() => onSessionLost(signedOut), [signedOut])

  const value = useMemo<AuthState>(
    () => ({
      user,
      async login(username, password, remember) {
        const data = unwrap(await api.POST('/api/v1/auth/login', { body: { username, password, remember, client_kind: 'web' } }))
        queryClient.clear()
        scheduleRefresh(new Date(data.expires_at))
        setUser(data.user)
      },
      async activate(token, password) {
        const data = unwrap(await api.POST('/api/v1/auth/activate', { body: { token, password } }))
        queryClient.clear()
        scheduleRefresh(new Date(data.expires_at))
        setUser(data.user)
      },
      async logout() {
        try {
          unwrapEmpty(await api.POST('/api/v1/auth/logout'))
        } catch (e) {
          if (!(e instanceof ApiError) || e.status !== 401) throw e
        }
        signedOut()
      },
    }),
    [user, queryClient, signedOut],
  )
  return <AuthContext value={value}>{children}</AuthContext>
}
