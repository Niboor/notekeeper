import { useEffect, useRef, useState } from 'react'
import { Link, Outlet } from 'react-router'
import { useAuth } from '../auth/AuthProvider'
import { t } from '../i18n'
import { Icon } from './Icon'
import { useTheme } from './theme'

function useOnline(): boolean {
  const [online, setOnline] = useState(() => navigator.onLine)
  useEffect(() => {
    const up = () => setOnline(true)
    const down = () => setOnline(false)
    window.addEventListener('online', up)
    window.addEventListener('offline', down)
    return () => {
      window.removeEventListener('online', up)
      window.removeEventListener('offline', down)
    }
  }, [])
  return online
}

/** The signed-in frame: top bar with brand, theme toggle and account menu, and an offline banner. */
export function Layout() {
  const { user, logout } = useAuth()
  const [theme, toggleTheme] = useTheme()
  const online = useOnline()
  const [open, setOpen] = useState(false)
  const menuRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const close = (e: MouseEvent | KeyboardEvent) => {
      if (e instanceof KeyboardEvent ? e.key === 'Escape' : !menuRef.current?.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', close)
    document.addEventListener('keydown', close)
    return () => {
      document.removeEventListener('mousedown', close)
      document.removeEventListener('keydown', close)
    }
  }, [open])

  const initials = (user?.display_name ?? '?').slice(0, 1).toUpperCase()
  return (
    <div className="app">
      <header className="topbar">
        <Link to="/" className="brand" style={{ textDecoration: 'none' }}>
          <span className="logo" />
          <span className="brand-name">{t('app.name')}</span>
        </Link>
        <nav className="pages" aria-label="Pages" />
        <div className="actions">
          <button className="icon-btn" onClick={toggleTheme} aria-label={t('nav.theme')} title={t('nav.theme')}>
            <Icon name={theme === 'dark' ? 'sun' : 'moon'} />
          </button>
          <div className="menu-anchor" ref={menuRef}>
            <button className="avatar" onClick={() => setOpen((v) => !v)} aria-haspopup="menu" aria-expanded={open} aria-label={t('nav.signedInAs', { name: user?.display_name ?? '' })}>
              {initials}
            </button>
            {open && (
              <div className="user-menu" role="menu">
                <div className="who">{t('nav.signedInAs', { name: user?.display_name ?? '' })}</div>
                <Link to="/settings" role="menuitem" onClick={() => setOpen(false)}>
                  {t('nav.settings')}
                </Link>
                <button role="menuitem" onClick={() => void logout()}>
                  {t('nav.signOut')}
                </button>
              </div>
            )}
          </div>
        </div>
      </header>
      {!online && (
        <div className="banner" role="status">
          {t('common.offline')}
        </div>
      )}
      <Outlet />
    </div>
  )
}
