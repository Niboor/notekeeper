import { useEffect, useRef } from 'react'
import { api } from '../api/client'
import { useAuth } from './AuthProvider'

/**
 * A new account starts in UTC; the first time it is used, the browser's zone is adopted (CORE-R2). The
 * attempt is marked in the settings (`tz_auto`) so that it happens once, whatever the answer, and a
 * person who later picks UTC on purpose keeps it. A guard also prevents a second request while the
 * first is in flight or if the profile update comes back without the mark.
 */
export function useAdoptBrowserTimezone(): void {
  const { user, updateUser } = useAuth()
  const tried = useRef(false)
  useEffect(() => {
    if (!user || tried.current || user.timezone !== 'UTC') return
    const settings = user.settings as { tz_auto?: boolean }
    if (settings.tz_auto) return
    tried.current = true
    const zone = Intl.DateTimeFormat().resolvedOptions().timeZone
    void api
      .PATCH('/api/v1/me', { body: { timezone: zone || 'UTC', settings: { ...settings, tz_auto: true } } })
      .then((res) => res.data && updateUser(res.data))
  }, [user, updateUser])
}
