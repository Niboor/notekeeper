import { useEffect, useState } from 'react'
import { NoteContent } from '../../components/NoteContent'
import { Icon } from '../../components/Icon'
import { t } from '../../i18n'
import type { components } from '../../api/public.schema'
import { formatWhen } from './ShareDialog'

type Shared = components['schemas']['SharedNote']
type Part = components['schemas']['SharedPart']

const ENDPOINT = '/api/public/v1/share'

/**
 * Fetches from the public share API with the token in a header: no cookies, no referrer, and the
 * token never in a URL that a server or a log could see (CORE-SH10, SEC-SHR-7).
 */
function publicFetch(path: string, token: string): Promise<Response> {
  return fetch(path, { headers: { 'X-Share-Token': token }, credentials: 'omit', referrerPolicy: 'no-referrer', cache: 'no-store' })
}

export function tokenFromLocation(hash: string): string {
  return hash.startsWith('#') ? hash.slice(1) : hash
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

/** The read-only page a link opens (CORE-SH4, CORE-SH10). It is served without the app's session and shows nothing but the note. */
export function SharePage() {
  const token = tokenFromLocation(window.location.hash)
  const [state, setState] = useState<{ status: 'loading' } | { status: 'gone' } | { status: 'ok'; note: Shared }>(token ? { status: 'loading' } : { status: 'gone' })

  useEffect(() => {
    document.title = t('shared.title')
    let cancelled = false
    if (!token) return
    publicFetch(ENDPOINT, token)
      .then(async (res) => {
        if (cancelled) return
        setState(res.ok ? { status: 'ok', note: (await res.json()) as Shared } : { status: 'gone' })
      })
      .catch(() => !cancelled && setState({ status: 'gone' }))
    return () => {
      cancelled = true
    }
  }, [token])

  return (
    <main className="share-page">
      <div className="share-brand">
        <span className="logo" />
        <span className="brand-name">{t('app.name')}</span>
      </div>
      <p className="share-notice" role="note">
        {t('shared.notice')}
      </p>
      {state.status === 'loading' && <p>{t('shared.loading')}</p>}
      {state.status === 'gone' && (
        <p role="alert" className="share-gone">
          {t('shared.gone')}
        </p>
      )}
      {state.status === 'ok' && (
        <article className="note share-note">
          <div className="note-body">
            {state.note.parts.map((p, i) => (
              <SharedPart key={i} part={p} token={token} />
            ))}
          </div>
          <div className="note-meta">
            <span>{t('shared.created', { when: formatWhen(state.note.created_at) })}</span>
            <span>{t('shared.until', { when: formatWhen(state.note.expires_at) })}</span>
          </div>
        </article>
      )}
    </main>
  )
}

function SharedPart({ part, token }: { part: Part; token: string }) {
  switch (part.kind) {
    case 'text':
    case 'unsupported':
      return part.kind === 'text' ? <NoteContent text={part.text ?? ''} /> : <p className="text muted">{part.text ?? t('note.unsupported')}</p>
    case 'failed_attachment':
      return (
        <div className="file">
          <Icon name="file" />
          <span className="fname">{t('shared.failedFile', { name: part.failed_filename ?? '' })}</span>
        </div>
      )
    default:
      return part.attachment ? <SharedFile file={part.attachment} token={token} /> : null
  }
}

const INLINE_IMAGE = /^image\/(png|jpe?g|gif|webp|avif)$/

/** Files are fetched with the token in a header and shown through a blob URL, so no address ever carries the token. */
function SharedFile({ file, token }: { file: NonNullable<Part['attachment']>; token: string }) {
  const path = `${ENDPOINT}/attachments/${file.id}`
  const [src, setSrc] = useState<string | null>(null)
  const image = INLINE_IMAGE.test(file.media_type)

  useEffect(() => {
    if (!image) return
    let url: string | undefined
    let cancelled = false
    void publicFetch(path, token)
      .then((r) => (r.ok ? r.blob() : Promise.reject(new Error('unavailable'))))
      .then((b) => {
        if (cancelled) return
        url = URL.createObjectURL(b)
        setSrc(url)
      })
      .catch(() => undefined)
    return () => {
      cancelled = true
      if (url) URL.revokeObjectURL(url)
    }
  }, [image, path, token])

  const download = async () => {
    const res = await publicFetch(path, token)
    if (!res.ok) return
    const url = URL.createObjectURL(await res.blob())
    const a = document.createElement('a')
    a.href = url
    a.download = file.filename
    a.rel = 'noopener'
    a.click()
    setTimeout(() => URL.revokeObjectURL(url), 10_000)
  }

  if (image) return src ? <img className="attach-img" src={src} alt={file.filename} /> : <div className="file"><span className="fname">{file.filename}</span></div>
  return (
    <button className="file" onClick={() => void download()} aria-label={t('shared.download', { name: file.filename })}>
      <Icon name="file" />
      <span className="fname">{file.filename}</span>
      <span className="fsize">{formatSize(file.size)}</span>
    </button>
  )
}
