import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { BrowserRouter, Navigate, Route, Routes, useLocation } from 'react-router'
import { api } from './api/client'
import { AuthProvider, useAuth } from './auth/AuthProvider'
import { ToastProvider } from './components/Toast'
import { Layout } from './components/Layout'
import { BoardShell } from './features/board/BoardShell'
import { SearchPage } from './features/search/SearchPage'
import { TrashPage } from './features/trash/TrashPage'
import { NoteLink } from './features/notes/NoteLink'
import { RemindersPage } from './features/reminders/RemindersPage'
import { SettingsPage } from './features/settings/SettingsPage'
import { t } from './i18n'
import { ActivatePage } from './pages/ActivatePage'
import { LoginPage } from './pages/LoginPage'
import { useEvents } from './realtime/useEvents'

/** A new account starts in UTC; the first time it is used, the browser's zone is adopted (CORE-R2). */
function useAdoptBrowserTimezone() {
  const { user, updateUser } = useAuth()
  useEffect(() => {
    if (!user || user.timezone !== 'UTC') return
    const settings = user.settings as { tz_auto?: boolean }
    if (settings.tz_auto) return
    const zone = Intl.DateTimeFormat().resolvedOptions().timeZone
    void api
      .PATCH('/api/v1/me', { body: { timezone: zone || 'UTC', settings: { ...settings, tz_auto: true } } })
      .then((res) => res.data && updateUser(res.data))
  }, [user, updateUser])
}

function RequireAuth() {
  const { user } = useAuth()
  useAdoptBrowserTimezone()
  const location = useLocation()
  useEvents(!!user)
  if (user === undefined) return <div className="spinner-page">{t('common.loading')}</div>
  if (user === null) return <Navigate to="/login" replace state={{ from: location.pathname }} />
  return <Layout />
}

export function App() {
  const [queryClient] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: {
            staleTime: 30_000,
            retry: (count, err) => count < 2 && !(err as { status?: number }).status,
            refetchOnWindowFocus: false, // the event stream keeps views current
          },
        },
      }),
  )
  return (
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <AuthProvider>
          <ToastProvider>
          <Routes>
            <Route path="/login" element={<LoginPage />} />
            <Route path="/activate" element={<ActivatePage />} />
            <Route element={<RequireAuth />}>
              <Route path="/" element={<BoardShell />} />
              <Route path="/p/:pageId" element={<BoardShell />} />
              <Route path="/trash" element={<TrashPage />} />
              <Route path="/search" element={<SearchPage />} />
              <Route path="/settings" element={<SettingsPage />} />
              <Route path="/reminders" element={<RemindersPage />} />
              <Route path="/notes/:id" element={<NoteLink />} />
            </Route>
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
          </ToastProvider>
        </AuthProvider>
      </BrowserRouter>
    </QueryClientProvider>
  )
}
