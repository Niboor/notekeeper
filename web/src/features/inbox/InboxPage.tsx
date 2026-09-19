import { useInfiniteQuery } from '@tanstack/react-query'
import { api, unwrap } from '../../api/client'
import { NoteCard } from '../../components/NoteCard'
import { t } from '../../i18n'

/** The Inbox as a persistent tray with a live count (WEB-2). The board of pages and columns joins it in milestone M2. */
export function InboxPage() {
  const q = useInfiniteQuery({
    queryKey: ['inbox'],
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) =>
      unwrap(await api.GET('/api/v1/inbox/notes', { params: { query: { cursor: pageParam, limit: 50 } } })),
    getNextPageParam: (last) => last.next_cursor,
  })
  const notes = q.data?.pages.flatMap((p) => p.items) ?? []
  const total = q.data?.pages[0]?.total

  return (
    <div className="layout">
      <section className="inbox lane" aria-label={t('inbox.title')}>
        <div className="lane-head">
          <h2>{t('inbox.title')}</h2>
          {total !== undefined && <span className="count">{total}</span>}
        </div>
        <div className="lane-notes">
          {q.isPending && <p className="empty">{t('common.loading')}</p>}
          {q.isError && <p className="empty">{t('common.error')}</p>}
          {q.isSuccess && notes.length === 0 && <p className="empty">{t('inbox.empty')}</p>}
          {notes.map((n) => (
            <NoteCard key={n.id} note={n} />
          ))}
          {q.hasNextPage && (
            <button className="btn load-more" onClick={() => void q.fetchNextPage()} disabled={q.isFetchingNextPage}>
              {t('inbox.loadMore')}
            </button>
          )}
        </div>
      </section>
      <div className="board-hint">
        <div>
          <strong>{t('inbox.boardHint.title')}</strong>
          <p>{t('inbox.boardHint.body')}</p>
        </div>
      </div>
    </div>
  )
}
