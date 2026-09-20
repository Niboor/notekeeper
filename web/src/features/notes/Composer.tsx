import { useEffect, useRef, useState } from 'react'
import { ApiError } from '../../api/client'
import { Icon } from '../../components/Icon'
import { useAutoGrow } from '../../components/useAutoGrow'
import { t } from '../../i18n'
import { uploadFile, useCreateNote, type UploadedFile } from '../hooks'

interface Pending {
  key: string
  name: string
  state: 'uploading' | 'done' | 'failed'
  file?: UploadedFile
  error?: string
}

interface Props {
  /** null creates the note in the Inbox. */
  categoryId: string | null
  onClose: () => void
}

/**
 * The inline composer (WEB-9): text (Markdown, "- [ ]" lines make a checklist) and attachments.
 * Files upload as soon as they are chosen, so adding the note is instant. Ctrl+Enter adds, Esc cancels.
 */
export function Composer({ categoryId, onClose }: Props) {
  const [text, setText] = useState('')
  const [files, setFiles] = useState<Pending[]>([])
  const create = useCreateNote()
  // One id for this note, however often adding is tried: a retry after a lost answer cannot make two notes.
  const noteId = useRef(crypto.randomUUID())
  const [failure, setFailure] = useState<string | null>(null)
  const area = useRef<HTMLTextAreaElement>(null)
  const input = useRef<HTMLInputElement>(null)
  useEffect(() => area.current?.focus(), [])
  useAutoGrow(area, text)

  const ready = files.filter((f) => f.state === 'done' && f.file).map((f) => f.file!.id)
  const uploading = files.some((f) => f.state === 'uploading')
  const canAdd = (text.trim() !== '' || ready.length > 0) && !uploading

  // The composer stays open until the server has the note: when adding fails (offline, signed out, the
  // account is full) the text and the uploaded files are still here to try again (CR-050).
  const saving = create.isPending
  const add = () => {
    if (!canAdd || saving) return
    setFailure(null)
    create.mutate(
      { id: noteId.current, categoryId, text, attachmentIds: ready },
      {
        onSuccess: onClose,
        onError: () => setFailure(t('composer.failed')),
      },
    )
  }

  const choose = (list: FileList | null) => {
    for (const file of Array.from(list ?? [])) {
      const key = crypto.randomUUID()
      setFiles((f) => [...f, { key, name: file.name, state: 'uploading' }])
      uploadFile(file).then(
        (uploaded) => setFiles((f) => f.map((x) => (x.key === key ? { ...x, state: 'done', file: uploaded } : x))),
        (err: unknown) => {
          const code = err instanceof ApiError ? err.code : ''
          const error = code === 'too_large' ? t('composer.tooLarge', { name: file.name }) : code === 'quota_exceeded' ? t('composer.quota', { name: file.name }) : t('composer.uploadFailed', { name: file.name })
          setFiles((f) => f.map((x) => (x.key === key ? { ...x, state: 'failed', error } : x)))
        },
      )
    }
    if (input.current) input.current.value = ''
  }

  return (
    <div className="composer" onKeyDown={(e) => e.stopPropagation()}>
      <textarea
        ref={area}
        value={text}
        placeholder={t('composer.placeholder')}
        aria-label={t('composer.placeholder')}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Escape' && !saving) onClose()
          if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
            e.preventDefault()
            add()
          }
        }}
      />
      {files.length > 0 && (
        <ul className="pending-files">
          {files.map((f) => (
            <li key={f.key} className={f.state === 'failed' ? 'failed' : undefined}>
              <Icon name="file" />
              <span className="fname">{f.state === 'failed' ? f.error : f.state === 'uploading' ? t('composer.uploading', { name: f.name }) : f.name}</span>
              <button className="icon-btn" aria-label={t('composer.remove', { name: f.name })} onClick={() => setFiles((all) => all.filter((x) => x.key !== f.key))}>
                <Icon name="x" />
              </button>
            </li>
          ))}
        </ul>
      )}
      {failure && (
        <p className="composer-error" role="alert">
          {failure}
        </p>
      )}
      <div className="composer-bar">
        <input ref={input} type="file" multiple hidden onChange={(e) => choose(e.target.files)} />
        <button className="icon-btn" aria-label={t('composer.attach')} title={t('composer.attach')} onClick={() => input.current?.click()}>
          <Icon name="clip" />
        </button>
        <span className="grow" />
        <span className="hint">{t('composer.hint')}</span>
        <button className="btn" onClick={onClose} disabled={saving}>
          {t('common.cancel')}
        </button>
        <button className="btn primary" onClick={add} disabled={!canAdd || saving}>
          {t('composer.submit')}
        </button>
      </div>
    </div>
  )
}
