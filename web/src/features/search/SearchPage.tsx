import { useInfiniteQuery } from '@tanstack/react-query'
import { Link, useSearchParams } from 'react-router'
import { api, unwrap } from '../../api/client'
import { NoteContent } from '../../components/NoteContent'
import { t } from '../../i18n'
import { AttachmentView } from '../notes/AttachmentView'

const MARK_START = ''
const MARK_END = ''

/** Turns the server's private-use match markers into <mark> elements. No markup ever comes from the server. */
export function Highlighted({ text }: { text: string }) {
  const out: React.ReactNode[] = []
  let i = 0
  let key = 0
  while (i < text.length) {
    const s = text.indexOf(MARK_START, i)
    if (s === -1) {
      out.push(text.slice(i))
      break
    }
    if (s > i) out.push(text.slice(i, s))
    const e = text.indexOf(MARK_END, s + 1)
    if (e === -1) {
      out.push(text.slice(s + 1))
      break
    }
    out.push(<mark key={key++}>{text.slice(s + 1, e)}</mark>)
    i = e + 1
  }
  return <>{out}</>
}

type Scope = 'active' | 'trash' | 'all'

/** Global search (WEB-14) over notes, the Trash, or everything. */
export function SearchPage() {
  const [params, setParams] = useSearchParams()
  const q = params.get('q') ?? ''
  const scope = (params.get('scope') as Scope | null) ?? 'active'
  const results = useInfiniteQuery({
    queryKey: ['search', q, scope],
    enabled: q.trim() !== '',
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => unwrap(await api.GET('/api/v1/search', { params: { query: { q, scope, cursor: pageParam, limit: 30 } } })),
    getNextPageParam: (last) => last.next_cursor,
  })
  const hits = results.data?.pages.flatMap((p) => p.items) ?? []
  return (
    <div className="page-body">
      <div className="page-narrow">
        <h1>{t('search.title')}</h1>
        <div className="scope-tabs" role="group" aria-label={t('search.title')}>
          {(['active', 'trash', 'all'] as const).map((s) => (
            <button key={s} className={`btn${scope === s ? ' primary' : ''}`} aria-pressed={scope === s} onClick={() => setParams({ q, scope: s })}>
              {t(`search.scope.${s}`)}
            </button>
          ))}
        </div>
        {q.trim() === '' && <p className="empty">{t('search.prompt')}</p>}
        {results.isSuccess && hits.length === 0 && <p className="empty">{t('search.none', { q })}</p>}
        <div className="trash-list">
          {hits.map(({ note, snippet }) => (
            <article className="note" key={note.id}>
              <p className="snippet">
                <Highlighted text={snippet} />
              </p>
              <div className="note-body">
                {note.parts.map((p) =>
                  p.kind === 'text' ? <NoteContent key={p.id} text={p.text ?? ''} /> : p.kind === 'attachment' ? <AttachmentView key={p.id} part={p} /> : null,
                )}
              </div>
              <div className="note-meta">
                {note.previous_location?.page_id ? (
                  <Link to={`/p/${note.previous_location.page_id}`}>{t('search.in', { page: note.previous_location.page_name ?? '', column: note.previous_location.category_name ?? '' })}</Link>
                ) : (
                  <Link to="/">{t('search.inInbox')}</Link>
                )}
                {note.state === 'deleted' && <span> · {t('trash.title')}</span>}
              </div>
            </article>
          ))}
        </div>
        {results.hasNextPage && (
          <button className="btn load-more" onClick={() => void results.fetchNextPage()}>
            {t('search.more')}
          </button>
        )}
      </div>
    </div>
  )
}
