import { Icon } from '../../components/Icon'
import { t } from '../../i18n'
import type { NotePart } from '../types'

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

/**
 * One attachment on a card: images and media inline, everything else as a file chip. Files are
 * requested from the authenticated download endpoint (same origin, cookies), which serves only
 * safe types inline and everything else as a download (SEC-CNT-3).
 */
export function AttachmentView({ part }: { part: NotePart }) {
  const a = part.attachment
  if (!a) return null
  const url = `/api/v1/attachments/${a.id}`
  if (/^image\/(png|jpe?g|gif|webp|avif)$/.test(a.media_type)) {
    return (
      <a href={url} target="_blank" rel="noopener noreferrer" className="thumb-link">
        <img className="attach-img" src={url} alt={a.filename} loading="lazy" />
      </a>
    )
  }
  if (/^audio\/(mpeg|ogg|wav|webm|mp4|flac|aac)$/.test(a.media_type)) {
    return <audio className="attach-media" controls preload="none" src={url} aria-label={a.filename} />
  }
  if (/^video\/(mp4|webm|ogg)$/.test(a.media_type)) {
    return <video className="attach-media" controls preload="none" src={url} aria-label={a.filename} />
  }
  return (
    <a className="file" href={url} download={a.filename} aria-label={t('note.download', { name: a.filename })}>
      <Icon name="file" />
      <span className="fname">{a.filename}</span>
      <span className="fsize">{formatSize(a.size)}</span>
    </a>
  )
}
