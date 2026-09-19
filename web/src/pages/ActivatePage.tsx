import { useState, type FormEvent } from 'react'
import { Navigate } from 'react-router'
import { ApiError } from '../api/client'
import { useAuth } from '../auth/AuthProvider'
import { t } from '../i18n'

/** The activation token travels in the URL fragment, so it never reaches a server log (SEC-AUTH-12). */
export function tokenFromHash(hash: string): string {
  return hash.startsWith('#') ? hash.slice(1) : hash
}

export function ActivatePage() {
  const { user, activate } = useAuth()
  const [token] = useState(() => tokenFromHash(window.location.hash))
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  if (user) return <Navigate to="/" replace />

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      await activate(token, password)
      // Do not leave the single-use token in the address bar or history.
      window.history.replaceState(null, '', '/activate')
    } catch (err) {
      if (err instanceof ApiError && err.code === 'invalid_link') setError(t('activate.invalid'))
      else if (err instanceof ApiError && err.code === 'invalid_input') setError(t('activate.weak'))
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
        <h1>{t('activate.title')}</h1>
        {token ? (
          <>
            <p className="lead">{t('activate.lead')}</p>
            {error && (
              <p className="form-error" role="alert">
                {error}
              </p>
            )}
            <div className="field">
              <label htmlFor="new-password">{t('activate.password')}</label>
              <input id="new-password" type="password" autoComplete="new-password" autoFocus required minLength={10} value={password} onChange={(e) => setPassword(e.target.value)} />
              <span className="hint">{t('activate.passwordHint')}</span>
            </div>
            <button className="btn primary block" type="submit" disabled={busy}>
              {t('activate.submit')}
            </button>
          </>
        ) : (
          <p className="lead">{t('activate.missing')}</p>
        )}
      </form>
    </main>
  )
}
