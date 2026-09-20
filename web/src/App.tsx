import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { useState } from 'react'
import { BrowserRouter, Navigate, Route, Routes, useLocation } from 'react-router'
import { AuthProvider, useAuth } from './auth/AuthProvider'
import { useAdoptBrowserTimezone } from './auth/useAdoptBrowserTimezone'
import { useOnline } from './components/useOnline'
import { ToastProvider } from './components/Toast'
import { Layout } from './components/Layout'
import { BoardShell } from './features/board/BoardShell'
import { SearchPage } from './features/search/SearchPage'
import { TrashPage } from './features/trash/TrashPage'
import { AdminPage } from './features/admin/AdminPage'
import { NotFound } from './features/NotFound'
import { NoteLink } from './features/notes/NoteLink'
import { RemindersPage } from './features/reminders/RemindersPage'
import { SettingsPage } from './features/settings/SettingsPage'
import { t } from './i18n'
import { ActivatePage } from './pages/ActivatePage'
import { LoginPage } from './pages/LoginPage'
import { useEvents } from './realtime/useEvents'

function RequireAuth() {
  const { user } = useAuth()
  useAdoptBrowserTimezone()
  const online = useOnline()
  const location = useLocation()
  useEvents(!!user)
  if (user === undefined) return <div className="spinner-page">{online ? t('common.loading') : t('common.offline')}</div>
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
              <Route path="/admin" element={<AdminPage />} />
              <Route path="/reminders" element={<RemindersPage />} />
              <Route path="/notes/:id" element={<NoteLink />} />
              <Route path="*" element={<NotFound />} />
            </Route>
          </Routes>
          </ToastProvider>
        </AuthProvider>
      </BrowserRouter>
    </QueryClientProvider>
  )
}
