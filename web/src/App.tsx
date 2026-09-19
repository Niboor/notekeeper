import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { useState } from 'react'
import { BrowserRouter, Navigate, Route, Routes, useLocation } from 'react-router'
import { AuthProvider, useAuth } from './auth/AuthProvider'
import { Layout } from './components/Layout'
import { InboxPage } from './features/inbox/InboxPage'
import { SettingsPage } from './features/settings/SettingsPage'
import { t } from './i18n'
import { ActivatePage } from './pages/ActivatePage'
import { LoginPage } from './pages/LoginPage'
import { useEvents } from './realtime/useEvents'

function RequireAuth() {
  const { user } = useAuth()
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
          <Routes>
            <Route path="/login" element={<LoginPage />} />
            <Route path="/activate" element={<ActivatePage />} />
            <Route element={<RequireAuth />}>
              <Route path="/" element={<InboxPage />} />
              <Route path="/settings" element={<SettingsPage />} />
            </Route>
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </AuthProvider>
      </BrowserRouter>
    </QueryClientProvider>
  )
}
