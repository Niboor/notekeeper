import { useEffect, useRef, useState, type FormEvent } from 'react'
import { Link, Outlet, useNavigate, useSearchParams } from 'react-router'
import { useAuth } from '../auth/AuthProvider'
import { t } from '../i18n'
import { Icon } from './Icon'
import { useTheme } from './theme'
import { TopbarSlot } from './topbar'

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
  const [slot, setSlot] = useState<HTMLElement | null>(null)
  const navigate = useNavigate()
  const [params] = useSearchParams()
  const searchRef = useRef<HTMLInputElement>(null)
  const [query, setQuery] = useState(params.get('q') ?? '')

  // "/" and Ctrl+K focus the search field from anywhere (WEB-14, WEB-16).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const inField = (e.target as HTMLElement).matches('input, textarea, select, [contenteditable]')
      if ((e.key === '/' && !inField) || (e.key.toLowerCase() === 'k' && (e.ctrlKey || e.metaKey))) {
        e.preventDefault()
        searchRef.current?.focus()
        searchRef.current?.select()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])
  const submitSearch = (e: FormEvent) => {
    e.preventDefault()
    if (query.trim()) void navigate(`/search?q=${encodeURIComponent(query.trim())}`)
  }

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
        <div ref={setSlot} className="pages" />
        <form className="search" role="search" onSubmit={submitSearch}>
          <Icon name="search" />
          <input ref={searchRef} type="search" value={query} onChange={(e) => setQuery(e.target.value)} placeholder={t('nav.searchPlaceholder')} aria-label={t('nav.search')} />
          <kbd>/</kbd>
        </form>
        <div className="actions">
          <button className="btn primary new-note" onClick={() => window.dispatchEvent(new Event('nk:new-note'))}>
            <Icon name="plus" />
            {t('nav.newNote')}
            <kbd>N</kbd>
          </button>
          <Link className="icon-btn" to="/trash" aria-label={t('nav.trash')} title={t('nav.trash')}>
            <Icon name="trash" />
          </Link>
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
      <TopbarSlot value={slot}>
        <Outlet />
      </TopbarSlot>
    </div>
  )
}
