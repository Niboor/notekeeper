import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { api, ApiError, unwrap, unwrapEmpty } from '../../api/client'
import { t } from '../../i18n'
import { formatWhen } from '../share/ShareDialog'
import { useRevokeAllShares, useRevokeShare, useShareLinks } from '../share/hooks'

/** Settings: linked chats (with pairing codes), signed-in devices, password. */
export function SettingsPage() {
  return (
    <div className="page-body">
      <div className="page-narrow">
        <h1>{t('settings.title')}</h1>
        <ChatsSection />
        <ShareLinksSection />
        <SessionsSection />
        <PasswordSection />
      </div>
    </div>
  )
}

function ChatsSection() {
  const qc = useQueryClient()
  const identities = useQuery({ queryKey: ['identities'], queryFn: async () => unwrap(await api.GET('/api/v1/me/identities')) })
  const bots = useQuery({ queryKey: ['bot-instances'], queryFn: async () => unwrap(await api.GET('/api/v1/bot-instances')) })
  const [code, setCode] = useState<{ code: string; bot: string } | null>(null)
  const pair = useMutation({
    mutationFn: async (bot: { id: string; name: string }) => {
      const res = unwrap(await api.POST('/api/v1/me/pairing-codes', { body: { bot_instance_id: bot.id } }))
      return { code: res.code, bot: bot.name }
    },
    onSuccess: setCode,
  })
  const unlink = useMutation({
    mutationFn: async (id: string) => unwrapEmpty(await api.DELETE('/api/v1/me/identities/{id}', { params: { path: { id } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['identities'] }),
  })

  return (
    <section className="section" aria-labelledby="chats-h">
      <h2 id="chats-h">{t('settings.chats.title')}</h2>
      <p>{t('settings.chats.lead')}</p>
      {identities.data?.items.length === 0 && <p>{t('settings.chats.none')}</p>}
      {identities.data?.items.map((i) => (
        <div className="row" key={i.id}>
          <div className="grow">
            {i.external_user_id}
            <div className="sub">{i.bot_instance_name}</div>
          </div>
          <button
            className="btn"
            onClick={() => {
              if (window.confirm(t('settings.chats.unlinkConfirm', { name: i.external_user_id }))) unlink.mutate(i.id)
            }}
          >
            {t('settings.chats.unlink')}
          </button>
        </div>
      ))}
      <h3 style={{ margin: '18px 0 6px', fontSize: 14 }}>{t('settings.chats.link')}</h3>
      {bots.data?.items.length === 0 && <p>{t('settings.chats.noBots')}</p>}
      {bots.data?.items.map((b) => (
        <div className="row" key={b.id}>
          <div className="grow">
            {b.name}
            <div className="sub">
              <span className={`status-dot ${b.online ? 'on' : ''}`} />
              {b.online ? t('settings.chats.online') : t('settings.chats.offline')}
            </div>
          </div>
          <button className="btn primary" onClick={() => pair.mutate(b)} disabled={pair.isPending}>
            {t('settings.chats.link')}
          </button>
        </div>
      ))}
      {code && (
        <div style={{ marginTop: 14 }}>
          <p>{t('settings.chats.codeIntro', { bot: code.bot })}</p>
          <span className="code">!link {code.code}</span>
        </div>
      )}
    </section>
  )
}

function SessionsSection() {
  const qc = useQueryClient()
  const sessions = useQuery({ queryKey: ['sessions'], queryFn: async () => unwrap(await api.GET('/api/v1/me/sessions')) })
  const refresh = () => void qc.invalidateQueries({ queryKey: ['sessions'] })
  const revoke = useMutation({
    mutationFn: async (id: string) => unwrapEmpty(await api.DELETE('/api/v1/me/sessions/{id}', { params: { path: { id } } })),
    onSuccess: refresh,
  })
  const revokeOthers = useMutation({
    mutationFn: async () => unwrapEmpty(await api.POST('/api/v1/me/sessions/revoke-all', { body: { keep_current: true } })),
    onSuccess: refresh,
  })
  return (
    <section className="section" aria-labelledby="sessions-h">
      <h2 id="sessions-h">{t('settings.sessions.title')}</h2>
      {sessions.data?.items.map((s) => (
        <div className="row" key={s.id}>
          <div className="grow">
            {s.label} {s.current && <span className="sub">({t('settings.sessions.current')})</span>}
            <div className="sub">{new Date(s.last_used_at).toLocaleString('en')}</div>
          </div>
          {!s.current && (
            <button className="btn" onClick={() => revoke.mutate(s.id)}>
              {t('settings.sessions.revoke')}
            </button>
          )}
        </div>
      ))}
      <p style={{ marginTop: 12 }}>
        <button className="btn" onClick={() => revokeOthers.mutate()}>
          {t('settings.sessions.revokeAll')}
        </button>
      </p>
    </section>
  )
}

function PasswordSection() {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [state, setState] = useState<'idle' | 'done' | 'wrong' | 'weak' | 'error'>('idle')
  async function submit(e: FormEvent) {
    e.preventDefault()
    try {
      unwrapEmpty(await api.POST('/api/v1/me/password', { body: { current_password: current, new_password: next } }))
      setState('done')
      setCurrent('')
      setNext('')
    } catch (err) {
      if (err instanceof ApiError && err.code === 'invalid_credentials') setState('wrong')
      else if (err instanceof ApiError && err.code === 'invalid_input') setState('weak')
      else setState('error')
    }
  }
  return (
    <section className="section" aria-labelledby="password-h">
      <h2 id="password-h">{t('settings.password.title')}</h2>
      <form onSubmit={(e) => void submit(e)} style={{ maxWidth: 360 }}>
        {state === 'done' && <p role="status">{t('settings.password.done')}</p>}
        {state === 'wrong' && <p className="form-error" role="alert">{t('settings.password.wrong')}</p>}
        {state === 'weak' && <p className="form-error" role="alert">{t('activate.weak')}</p>}
        {state === 'error' && <p className="form-error" role="alert">{t('common.error')}</p>}
        <div className="field">
          <label htmlFor="cur-pw">{t('settings.password.current')}</label>
          <input id="cur-pw" type="password" autoComplete="current-password" required value={current} onChange={(e) => setCurrent(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="new-pw">{t('settings.password.new')}</label>
          <input id="new-pw" type="password" autoComplete="new-password" required minLength={10} value={next} onChange={(e) => setNext(e.target.value)} />
        </div>
        <button className="btn primary" type="submit">
          {t('settings.password.submit')}
        </button>
      </form>
    </section>
  )
}

/** Every active share link with its usage, and the way to end them (WEB-12, WEB-21, CORE-SH3, CORE-SH14). */
function ShareLinksSection() {
  const links = useShareLinks()
  const revoke = useRevokeShare()
  const revokeAll = useRevokeAllShares()
  const [confirming, setConfirming] = useState(false)
  const items = links.data?.items ?? []
  return (
    <section className="section" aria-labelledby="share-links-h">
      <h2 id="share-links-h">{t('settings.share.title')}</h2>
      <p>{t('settings.share.lead')}</p>
      {links.data && items.length === 0 && <p>{t('settings.share.none')}</p>}
      {items.map((l) => (
        <div className="row" key={l.id}>
          <div className="grow">
            {l.excerpt || '…'}
            <div className="sub">
              {t('share.until', { when: formatWhen(l.expires_at) })} · {t('share.views', { count: l.view_count })} ·{' '}
              {l.last_accessed_at ? t('settings.share.lastAccess', { when: formatWhen(l.last_accessed_at) }) : t('settings.share.never')}
              {!l.note_active && ` · ${t('settings.share.notWorking')}`}
            </div>
          </div>
          <button className="btn" onClick={() => revoke.mutate(l.id)} aria-label={`${t('share.revoke')}: ${l.excerpt}`}>
            {t('share.revoke')}
          </button>
        </div>
      ))}
      {items.length > 0 &&
        (confirming ? (
          <button
            className="btn danger"
            onClick={() => {
              revokeAll.mutate(undefined, { onSettled: () => setConfirming(false) })
            }}
          >
            {t('settings.share.confirmAll', { count: items.length })}
          </button>
        ) : (
          <button className="btn" onClick={() => setConfirming(true)}>
            {t('settings.share.revokeAll')}
          </button>
        ))}
    </section>
  )
}
