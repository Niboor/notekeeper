import { useQuery } from '@tanstack/react-query'
import { Navigate, useParams } from 'react-router'
import { api, unwrap } from '../../api/client'
import { t } from '../../i18n'
import { usePages } from '../hooks'

/** The address in a reminder: finds where the note is now and goes there (CORE-R6). */
export function NoteLink() {
  const { id = '' } = useParams()
  const note = useQuery({ queryKey: ['note', id], queryFn: async () => unwrap(await api.GET('/api/v1/notes/{id}', { params: { path: { id } } })), retry: false })
  const pages = usePages()
  if (note.isError) return <p className="page-body">{t('shared.gone')}</p>
  if (!note.data || !pages.data) return <div className="spinner-page">{t('common.loading')}</div>
  const n = note.data
  if (n.state === 'deleted') return <Navigate to={`/trash?note=${id}`} replace />
  const page = pages.data.find((p) => p.categories?.some((c) => c.id === n.category_id))
  return <Navigate to={page ? `/p/${page.id}?note=${id}` : `/?note=${id}`} replace />
}
