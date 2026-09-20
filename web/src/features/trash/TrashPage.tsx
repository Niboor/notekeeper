import { NoteContent } from '../../components/NoteContent'
import { timeAgo } from '../../components/time'
import { t } from '../../i18n'
import { AttachmentView } from '../notes/AttachmentView'
import { useDeleteForever, useRestoreNote, useTrash } from '../hooks'

/** The Trash (WEB-7): dismissed notes with where they came from, restore and delete for good. */
export function TrashPage() {
  const q = useTrash()
  const restore = useRestoreNote()
  const del = useDeleteForever()
  const notes = q.data?.pages.flatMap((p) => p.items) ?? []
  return (
    <div className="page-body">
      <div className="page-narrow">
        <h1>{t('trash.title')}</h1>
        {q.isSuccess && notes.length === 0 && <p className="empty">{t('trash.empty')}</p>}
        <div className="trash-list">
          {notes.map((n) => (
            <article className="note" key={n.id}>
              <div className="note-body">
                {n.parts.map((p) =>
                  p.kind === 'text' ? <NoteContent key={p.id} text={p.text ?? ''} /> : p.kind === 'attachment' ? <AttachmentView key={p.id} part={p} /> : null,
                )}
              </div>
              <div className="note-meta">
                <span>
                  {n.previous_location?.page_name
                    ? t('trash.cameFrom', { page: n.previous_location.page_name, column: n.previous_location.category_name ?? '' })
                    : t('trash.cameFromInbox')}
                  {n.deleted_at ? ` · ${timeAgo(n.deleted_at)}` : ''}
                </span>
              </div>
              <div className="composer-bar">
                <span className="grow" />
                <button className="btn" onClick={() => restore.mutate(n.id)}>
                  {t('trash.restore')}
                </button>
                <button
                  className="btn danger"
                  onClick={() => {
                    if (window.confirm(t('trash.deleteConfirm'))) del.mutate(n.id)
                  }}
                >
                  {t('trash.deleteForever')}
                </button>
              </div>
            </article>
          ))}
        </div>
        {q.hasNextPage && (
          <button className="btn load-more" onClick={() => void q.fetchNextPage()}>
            {t('inbox.loadMore')}
          </button>
        )}
      </div>
    </div>
  )
}
