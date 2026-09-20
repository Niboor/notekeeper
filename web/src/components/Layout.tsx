import { useEffect, useRef, useState, type FormEvent } from 'react'
import { Link, Outlet, useLocation, useNavigate, useSearchParams } from 'react-router'
import { useAuth } from '../auth/AuthProvider'
import { t } from '../i18n'
import { NotificationsBell } from '../features/reminders/NotificationsBell'
import { Icon } from './Icon'
import { Popover, useDismiss } from './Popover'
import { useOnline } from './useOnline'
import { useTheme } from './theme'
import { TopbarSlot } from './topbar'

/** The signed-in frame: top bar with brand, theme toggle and account menu, and an offline banner. */
export function Layout() {
  const { user, logout } = useAuth()
  const [theme, toggleTheme] = useTheme()
  const online = useOnline()
  const [open, setOpen] = useState(false)
  const avatarRef = useRef<HTMLButtonElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  const [slot, setSlot] = useState<HTMLElement | null>(null)
  const navigate = useNavigate()
  const [params] = useSearchParams()
  const searchRef = useRef<HTMLInputElement>(null)
  const { pathname } = useLocation()
  const [query, setQuery] = useState(params.get('q') ?? '')
  // The search box shows the query of the search page and is empty everywhere else.
  const shown = pathname === '/search' ? params.get('q') ?? '' : ''
  const [seen, setSeen] = useState(shown)
  if (seen !== shown) {
    setSeen(shown)
    setQuery(shown)
  }

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

  useDismiss(open, () => setOpen(false), [avatarRef, menuRef])

  const initials = (user?.display_name ?? '?').slice(0, 1).toUpperCase()
  return (
    <div className="app">
      <header className="topbar">
        <Link to="/" className="brand" style={{ textDecoration: 'none' }}>
          <span className="logo" />
          <span className="brand-name">{t('app.name')}</span>
        </Link>
        <div ref={setSlot} className="pages-slot" />
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
          <NotificationsBell />
          <Link className="icon-btn" to="/trash" aria-label={t('nav.trash')} title={t('nav.trash')}>
            <Icon name="trash" />
          </Link>
          <button className="icon-btn" onClick={toggleTheme} aria-label={t('nav.theme')} title={t('nav.theme')}>
            <Icon name={theme === 'dark' ? 'sun' : 'moon'} />
          </button>
          <div className="menu-anchor">
            <button ref={avatarRef} className="avatar" onClick={() => setOpen((v) => !v)} aria-haspopup="menu" aria-expanded={open} aria-label={t('nav.signedInAs', { name: user?.display_name ?? '' })}>
              {initials}
            </button>
            {open && (
              <Popover anchor={avatarRef} popRef={menuRef} className="user-menu" role="menu">
                <div className="who">{t('nav.signedInAs', { name: user?.display_name ?? '' })}</div>
                <Link to="/reminders" role="menuitem" onClick={() => setOpen(false)}>
                  {t('nav.reminders')}
                </Link>
                <Link to="/settings" role="menuitem" onClick={() => setOpen(false)}>
                  {t('nav.settings')}
                </Link>
                {user?.is_admin && (
                  <Link to="/admin" role="menuitem" onClick={() => setOpen(false)}>
                    {t('nav.admin')}
                  </Link>
                )}
                <button role="menuitem" onClick={() => void logout()}>
                  {t('nav.signOut')}
                </button>
              </Popover>
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
