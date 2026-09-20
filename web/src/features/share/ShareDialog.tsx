import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { ApiError } from '../../api/client'
import { useToast } from '../../components/Toast'
import { t, tn, type MessageKey } from '../../i18n'
import { useCreateShare, useRevokeShare, useShareLinks, type Expiry } from './hooks'

const EXPIRIES: { value: Expiry; seconds: number }[] = [
  { value: '1h', seconds: 3600 },
  { value: '1d', seconds: 86400 },
  { value: '7d', seconds: 7 * 86400 },
  { value: '30d', seconds: 30 * 86400 },
]

export function formatWhen(iso: string): string {
  return new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })
}

/** Create, copy and revoke share links of one note (WEB-21, CORE-SH1..SH3). */
export function ShareDialog({ noteId, onClose }: { noteId: string; onClose: () => void }) {
  const links = useShareLinks(noteId)
  const create = useCreateShare(noteId)
  const revoke = useRevokeShare()
  const { toast } = useToast()
  const ref = useRef<HTMLDialogElement>(null)
  const [expiry, setExpiry] = useState<Expiry>('7d')
  const [url, setUrl] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    const d = ref.current
    if (d && !d.open) {
      if (typeof d.showModal === 'function') d.showModal()
      else d.setAttribute('open', '')
    }
  }, [])

  const max = links.data?.max_lifetime_seconds ?? Infinity
  const choices = EXPIRIES.filter((e) => e.seconds <= max)
  const off = links.data?.enabled === false

  const submit = () => {
    setError(null)
    create.mutate(expiry, {
      onSuccess: (res) => setUrl(res.url),
      onError: (e) => {
        const code = e instanceof ApiError ? e.code : ''
        const key: MessageKey = code === 'share_limit' ? 'share.error.limit' : code === 'note_not_active' ? 'share.error.notActive' : 'share.error'
        setError(t(key))
      },
    })
  }

  const copy = async () => {
    if (!url) return
    try {
      await navigator.clipboard.writeText(url)
      toast({ message: t('share.copied') })
    } catch {
      // Clipboard access can be refused; the address is selectable in the field.
    }
  }

  // A portal keeps the dialog out of the note card's DOM, and the handlers below keep its clicks and keys
  // from reaching the card, where they would start a drag or a lift (React events cross portals).
  const stop = (e: React.SyntheticEvent) => e.stopPropagation()
  return createPortal(
    <dialog
      ref={ref}
      className="dialog"
      aria-labelledby="share-h"
      onClose={onClose}
      onCancel={onClose}
      onKeyDown={stop}
      onKeyUp={stop}
      onPointerDown={stop}
      onMouseDown={stop}
      onTouchStart={stop}
      onClick={stop}
    >
      <h2 id="share-h">{t('share.title')}</h2>
      {off ? (
        <p>{t('share.off')}</p>
      ) : (
        <>
          <p>{t('share.lead')}</p>
          <div className="row">
            <label htmlFor="share-expiry" className="grow">
              {t('share.expiry')}
            </label>
            <select id="share-expiry" value={expiry} onChange={(e) => setExpiry(e.target.value as Expiry)}>
              {choices.map((c) => (
                <option key={c.value} value={c.value}>
                  {t(`share.exp.${c.value}` as MessageKey)}
                </option>
              ))}
            </select>
            <button className="btn primary" onClick={submit} disabled={create.isPending}>
              {t('share.create')}
            </button>
          </div>
          {error && (
            <p className="form-error" role="alert">
              {error}
            </p>
          )}
          {url && (
            <div className="share-url" role="status">
              <p>{t('share.created')}</p>
              <input readOnly value={url} aria-label={t('share.copy')} onFocus={(e) => e.currentTarget.select()} />
              <div className="composer-bar">
                <button className="btn" onClick={() => void copy()}>
                  {t('share.copy')}
                </button>
                {typeof navigator.share === 'function' && (
                  <button className="btn" onClick={() => void navigator.share({ url }).catch(() => undefined)}>
                    {t('share.send')}
                  </button>
                )}
              </div>
            </div>
          )}
        </>
      )}
      <h3>{t('share.existing')}</h3>
      {links.data?.items.length === 0 && <p>{t('share.none')}</p>}
      <ul className="plain-list">
        {links.data?.items.map((l) => (
          <li className="row" key={l.id}>
            <div className="grow">
              {t('share.until', { when: formatWhen(l.expires_at) })}
              <div className="sub">{tn('share.views', l.view_count)}</div>
            </div>
            <button
              className="btn"
              onClick={() => revoke.mutate(l.id, { onSuccess: () => toast({ message: t('share.revoked') }) })}
              aria-label={`${t('share.revoke')} · ${t('share.until', { when: formatWhen(l.expires_at) })}`}
            >
              {t('share.revoke')}
            </button>
          </li>
        ))}
      </ul>
      <div className="composer-bar">
        <span className="grow" />
        <button className="btn" onClick={onClose}>
          {t('share.close')}
        </button>
      </div>
    </dialog>,
    document.body,
  )
}
