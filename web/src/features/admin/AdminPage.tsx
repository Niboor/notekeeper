import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { Navigate } from 'react-router'
import { api, ApiError, unwrap, unwrapEmpty, type Schemas } from '../../api/client'
import { useAuth } from '../../auth/AuthProvider'
import { t } from '../../i18n'
import { formatWhen } from '../share/ShareDialog'

type AdminUser = Schemas['AdminUser']
type BotInstance = Schemas['AdminBotInstance']

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = n
  let i = -1
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`
}

/** The admin section: accounts, storage and chat bots, and nothing that shows anybody's notes (WEB-19, AUTH-U6, SEC-ADM-1). */
export function AdminPage() {
  const { user } = useAuth()
  if (!user?.is_admin) return <Navigate to="/" replace />
  return (
    <div className="page-body">
      <div className="page-narrow wide">
        <h1>{t('admin.title')}</h1>
        <p>{t('admin.lead')}</p>
        <UsersSection />
        <BotsSection />
      </div>
    </div>
  )
}

function UsersSection() {
  const qc = useQueryClient()
  const users = useQuery({ queryKey: ['admin-users'], queryFn: async () => unwrap(await api.GET('/api/v1/admin/users')).items })
  const refresh = () => void qc.invalidateQueries({ queryKey: ['admin-users'] })
  const [name, setName] = useState('')
  const [link, setLink] = useState<{ user: string; url: string } | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null)

  const create = useMutation({
    mutationFn: async (username: string) => unwrap(await api.POST('/api/v1/admin/users', { body: { username } })),
    onSuccess: refresh,
  })
  const activation = useMutation({
    mutationFn: async (u: AdminUser) => ({ user: u.username, res: unwrap(await api.POST('/api/v1/admin/users/{id}/activation-link', { params: { path: { id: u.id } } })) }),
    onSuccess: ({ user, res }) => setLink({ user, url: `${window.location.origin}${res.path}` }),
  })
  const update = useMutation({
    mutationFn: async (v: { id: string; body: Schemas['AdminUpdateUser'] }) =>
      unwrapEmpty(await api.PATCH('/api/v1/admin/users/{id}', { params: { path: { id: v.id } }, body: v.body })),
    onSuccess: refresh,
  })
  const remove = useMutation({
    mutationFn: async (id: string) => unwrapEmpty(await api.DELETE('/api/v1/admin/users/{id}', { params: { path: { id } } })),
    onSuccess: () => {
      setConfirmDelete(null)
      refresh()
    },
  })

  const submit = (e: FormEvent) => {
    e.preventDefault()
    setError(null)
    create.mutate(name.trim().toLowerCase(), {
      onSuccess: (u) => {
        setName('')
        activation.mutate(u)
      },
      onError: (err) => setError(err instanceof ApiError && err.status === 409 ? t('admin.exists') : t('admin.error')),
    })
  }
  const setQuota = (u: AdminUser) => {
    const answer = window.prompt(t('admin.quotaPrompt', { name: u.username }), u.quota_bytes ? String(Math.round(u.quota_bytes / (1 << 20))) : '')
    if (answer === null) return
    if (answer.trim() === '') update.mutate({ id: u.id, body: { reset_quota: true } })
    else if (/^\d+$/.test(answer.trim())) update.mutate({ id: u.id, body: { quota_bytes: Number(answer.trim()) * (1 << 20) } })
  }

  return (
    <section className="section" aria-labelledby="admin-users-h">
      <h2 id="admin-users-h">{t('admin.users')}</h2>
      <form className="row form-row" onSubmit={submit}>
        <input value={name} onChange={(e) => setName(e.target.value)} placeholder={t('admin.newUser')} aria-label={t('admin.newUser')} maxLength={32} />
        <button className="btn primary" disabled={!name.trim() || create.isPending}>
          {t('admin.create')}
        </button>
      </form>
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      {link && (
        <div className="share-url" role="status">
          <p>{t('admin.linkFor', { name: link.user })}</p>
          <input readOnly value={link.url} aria-label={t('admin.activationLink')} onFocus={(e) => e.currentTarget.select()} />
        </div>
      )}
      <table className="admin-table">
        <thead>
          <tr>
            <th>{t('admin.col.user')}</th>
            <th>{t('admin.col.status')}</th>
            <th>{t('admin.col.storage')}</th>
            <th>{t('admin.col.since')}</th>
            <th>
              <span className="sr-only">{t('admin.col.actions')}</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {users.data?.map((u) => (
            <tr key={u.id}>
              <td>
                {u.username}
                {u.is_admin && <span className="chip">{t('admin.adminBadge')}</span>}
              </td>
              <td>{t(`admin.status.${u.status}` as 'admin.status.active')}</td>
              <td>
                {formatBytes(u.used_bytes)}
                {u.quota_bytes != null && ` / ${formatBytes(u.quota_bytes)}`}
              </td>
              <td>{formatWhen(u.created_at)}</td>
              <td className="row-actions">
                <button className="btn small" onClick={() => activation.mutate(u)} aria-label={`${t('admin.activation')}: ${u.username}`}>
                  {t('admin.activation')}
                </button>
                <button className="btn small" onClick={() => setQuota(u)} aria-label={`${t('admin.quota')}: ${u.username}`}>
                  {t('admin.quota')}
                </button>
                {!u.is_admin && u.status !== 'deleting' && (
                  <button
                    className="btn small"
                    onClick={() => update.mutate({ id: u.id, body: { disabled: u.status !== 'disabled' } })}
                    aria-label={`${u.status === 'disabled' ? t('admin.enable') : t('admin.disable')}: ${u.username}`}
                  >
                    {u.status === 'disabled' ? t('admin.enable') : t('admin.disable')}
                  </button>
                )}
                {!u.is_admin &&
                  u.status !== 'deleting' &&
                  (confirmDelete === u.id ? (
                    <button className="btn small danger" onClick={() => remove.mutate(u.id)}>
                      {t('admin.confirmDelete', { name: u.username })}
                    </button>
                  ) : (
                    <button className="btn small" onClick={() => setConfirmDelete(u.id)} aria-label={`${t('admin.delete')}: ${u.username}`}>
                      {t('admin.delete')}
                    </button>
                  ))}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  )
}

function BotsSection() {
  const qc = useQueryClient()
  const bots = useQuery({ queryKey: ['admin-bots'], queryFn: async () => unwrap(await api.GET('/api/v1/admin/bot-instances')).items })
  const refresh = () => void qc.invalidateQueries({ queryKey: ['admin-bots'] })
  const [name, setName] = useState('')
  const [domain, setDomain] = useState('')
  const [secret, setSecret] = useState<{ bot: string; bearer: string } | null>(null)

  const create = useMutation({
    mutationFn: async () => unwrap(await api.POST('/api/v1/admin/bot-instances', { body: { type: 'matrix', name: name.trim(), identity_domain: domain.trim() || undefined } })),
    onSuccess: () => {
      setName('')
      setDomain('')
      refresh()
    },
  })
  const setStatus = useMutation({
    mutationFn: async (v: { id: string; status: 'active' | 'disabled' }) =>
      unwrapEmpty(await api.PATCH('/api/v1/admin/bot-instances/{id}', { params: { path: { id: v.id } }, body: { status: v.status } })),
    onSuccess: refresh,
  })
  const issue = useMutation({
    mutationFn: async (b: BotInstance) => ({
      bot: b.name,
      res: unwrap(await api.POST('/api/v1/admin/bot-instances/{id}/credentials', { params: { path: { id: b.id } }, body: { scopes: ['ingest', 'deliver'] } })),
    }),
    onSuccess: ({ bot, res }) => {
      setSecret({ bot, bearer: res.bearer })
      refresh()
    },
  })
  const disableCred = useMutation({
    mutationFn: async (v: { id: string; credentialId: string }) =>
      unwrapEmpty(await api.DELETE('/api/v1/admin/bot-instances/{id}/credentials/{credentialId}', { params: { path: { id: v.id, credentialId: v.credentialId } } })),
    onSuccess: refresh,
  })

  return (
    <section className="section" aria-labelledby="admin-bots-h">
      <h2 id="admin-bots-h">{t('admin.bots')}</h2>
      <form
        className="row form-row"
        onSubmit={(e) => {
          e.preventDefault()
          create.mutate()
        }}
      >
        <input value={name} onChange={(e) => setName(e.target.value)} placeholder={t('admin.botName')} aria-label={t('admin.botName')} maxLength={64} />
        <input value={domain} onChange={(e) => setDomain(e.target.value)} placeholder={t('admin.botDomain')} aria-label={t('admin.botDomain')} maxLength={253} />
        <button className="btn primary" disabled={!name.trim() || create.isPending}>
          {t('admin.register')}
        </button>
      </form>
      {secret && (
        <div className="share-url" role="status">
          <p>{t('admin.secretFor', { name: secret.bot })}</p>
          <input readOnly value={secret.bearer} aria-label={t('admin.secret')} onFocus={(e) => e.currentTarget.select()} />
        </div>
      )}
      {bots.data?.map((b) => (
        <div className="bot-card" key={b.id}>
          <div className="row">
            <div className="grow">
              <strong>{b.name}</strong> <span className="sub">{b.type} · {b.identity_domain}{b.address ? ` · ${b.address}` : ''}</span>
              <div className="sub">
                {t(`admin.botStatus.${b.status}` as 'admin.botStatus.active')} · {b.last_seen_at ? t('admin.lastSeen', { when: formatWhen(b.last_seen_at) }) : t('admin.neverSeen')}
              </div>
            </div>
            <button className="btn small" onClick={() => issue.mutate(b)} aria-label={`${t('admin.newSecret')}: ${b.name}`}>
              {t('admin.newSecret')}
            </button>
            <button
              className="btn small"
              onClick={() => setStatus.mutate({ id: b.id, status: b.status === 'active' ? 'disabled' : 'active' })}
              aria-label={`${b.status === 'active' ? t('admin.disable') : t('admin.enable')}: ${b.name}`}
            >
              {b.status === 'active' ? t('admin.disable') : t('admin.enable')}
            </button>
          </div>
          <ul className="plain-list">
            {b.credentials?.map((c) => (
              <li className="row" key={c.id}>
                <code className="grow">{c.client_id}</code>
                <span className="sub">{c.scopes.join(', ')}</span>
                {c.disabled_at ? (
                  <span className="sub">{t('admin.credentialOff')}</span>
                ) : (
                  <button className="btn small" onClick={() => disableCred.mutate({ id: b.id, credentialId: c.id })} aria-label={`${t('admin.credentialDisable')}: ${c.client_id}`}>
                    {t('admin.credentialDisable')}
                  </button>
                )}
              </li>
            ))}
          </ul>
        </div>
      ))}
    </section>
  )
}
