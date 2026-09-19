import { useState, type FormEvent } from 'react'
import { Navigate, useLocation } from 'react-router'
import { ApiError } from '../api/client'
import { useAuth } from '../auth/AuthProvider'
import { t } from '../i18n'

export function LoginPage() {
  const { user, login } = useAuth()
  const location = useLocation()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [remember, setRemember] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  if (user) return <Navigate to={(location.state as { from?: string } | null)?.from ?? '/'} replace />

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      await login(username, password, remember)
    } catch (err) {
      if (err instanceof ApiError && err.status === 429) setError(t('login.throttled'))
      else if (err instanceof ApiError && err.status === 401) setError(t('login.invalid'))
      else setError(t('common.error'))
      setBusy(false)
    }
  }

  return (
    <main className="auth-page">
      <form className="auth-card" onSubmit={(e) => void submit(e)}>
        <div className="brand">
          <span className="logo" />
          <span className="brand-name">{t('app.name')}</span>
        </div>
        <h1>{t('login.title')}</h1>
        <p className="lead">{t('login.noAccount')}</p>
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        <div className="field">
          <label htmlFor="username">{t('login.username')}</label>
          <input id="username" type="text" autoComplete="username" autoCapitalize="none" autoFocus required value={username} onChange={(e) => setUsername(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="password">{t('login.password')}</label>
          <input id="password" type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} />
        </div>
        <label className="check">
          <input type="checkbox" checked={remember} onChange={(e) => setRemember(e.target.checked)} />
          {t('login.remember')}
        </label>
        <button className="btn primary block" type="submit" disabled={busy}>
          {t('login.submit')}
        </button>
      </form>
    </main>
  )
}
