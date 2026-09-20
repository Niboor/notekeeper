import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router'
import { Icon } from '../../components/Icon'
import { t } from '../../i18n'
import { formatWhen } from '../share/ShareDialog'
import { useMarkNotificationsRead, useNotifications, type AppNotification } from './hooks'

function describe(n: AppNotification): { text: string; to?: string } {
  const p = n.payload as Record<string, unknown>
  if (n.kind === 'reminder') return { text: t('bell.reminder', { text: String(p.excerpt ?? '') }), to: `/notes/${String(p.note_id)}` }
  if (n.kind === 'delivery_failed') return { text: t('bell.deliveryFailed') }
  return { text: String(p.text ?? '') }
}

/** The bell in the top bar: unread notifications (due reminders, security notices) and the way to the list of upcoming reminders (CORE-R9). */
export function NotificationsBell() {
  const { data } = useNotifications()
  const markRead = useMarkNotificationsRead()
  const [open, setOpen] = useState(false)
  const root = useRef<HTMLDivElement>(null)
  const unread = data?.unread ?? 0

  useEffect(() => {
    if (!open) return
    const close = (e: MouseEvent | KeyboardEvent) => {
      if (e instanceof KeyboardEvent ? e.key === 'Escape' : !root.current?.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', close)
    document.addEventListener('keydown', close)
    return () => {
      document.removeEventListener('mousedown', close)
      document.removeEventListener('keydown', close)
    }
  }, [open])

  return (
    <div className="menu-anchor" ref={root}>
      <button
        className="icon-btn bell"
        onClick={() => setOpen((v) => !v)}
        aria-haspopup="true"
        aria-expanded={open}
        aria-label={unread > 0 ? `${t('bell.label')}, ${t('bell.unread', { count: unread })}` : t('bell.label')}
        title={t('bell.label')}
      >
        <Icon name="bell" />
        {unread > 0 && <span className="badge" aria-hidden="true">{unread > 9 ? '9+' : unread}</span>}
      </button>
      {open && (
        <div className="user-menu notifications" role="region" aria-label={t('bell.label')}>
          {data && data.items.length === 0 && <div className="who">{t('bell.none')}</div>}
          <ul className="plain-list">
            {data?.items.slice(0, 15).map((n) => {
              const d = describe(n)
              return (
                <li key={n.id} className={n.read_at ? 'read' : 'unread'}>
                  {d.to ? (
                    <Link to={d.to} onClick={() => setOpen(false)}>
                      {d.text}
                    </Link>
                  ) : (
                    <span>{d.text}</span>
                  )}
                  <div className="sub">{formatWhen(n.created_at)}</div>
                </li>
              )
            })}
          </ul>
          <div className="notif-actions">
            <Link to="/reminders" onClick={() => setOpen(false)}>
              {t('bell.upcoming')}
            </Link>
            {unread > 0 && (
              <button className="btn small" onClick={() => markRead.mutate()}>
                {t('bell.markRead')}
              </button>
            )}
          </div>
        </div>
      )}
    </div>
  )
}
