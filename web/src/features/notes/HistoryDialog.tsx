import { useQuery } from '@tanstack/react-query'
import { useEffect, useRef } from 'react'
import { createPortal } from 'react-dom'
import { api, unwrap } from '../../api/client'
import { t } from '../../i18n'
import { useEditPart } from '../hooks'
import { formatWhen } from '../share/ShareDialog'
import type { Note } from '../types'

/** Earlier texts of a note, including ones a later edit replaced, with a way back (WEB-13, EDT-3). */
export function HistoryDialog({ note, onClose }: { note: Note; onClose: () => void }) {
  const history = useQuery({ queryKey: ['history', note.id, note.version], queryFn: async () => unwrap(await api.GET('/api/v1/notes/{id}/history', { params: { path: { id: note.id } } })) })
  const edit = useEditPart()
  const ref = useRef<HTMLDialogElement>(null)
  useEffect(() => {
    const d = ref.current
    if (d && !d.open) {
      if (typeof d.showModal === 'function') d.showModal()
      else d.setAttribute('open', '')
    }
  }, [])
  const stop = (e: React.SyntheticEvent) => e.stopPropagation()
  const parts = (history.data?.parts ?? []).filter((p) => p.versions.length > 0)
  return createPortal(
    <dialog ref={ref} className="dialog" aria-labelledby="history-h" onClose={onClose} onCancel={onClose} onKeyDown={stop} onKeyUp={stop} onPointerDown={stop} onMouseDown={stop} onTouchStart={stop} onClick={stop}>
      <h2 id="history-h">{t('history.title')}</h2>
      <p>{t('history.lead')}</p>
      {history.data && parts.length === 0 && <p>{t('history.none')}</p>}
      {parts.map((p, i) => {
        const current = note.parts.find((x) => x.id === p.part_id)?.text
        return (
          <section key={p.part_id} aria-label={t('history.part', { n: i + 1 })}>
            {parts.length > 1 && <h3>{t('history.part', { n: i + 1 })}</h3>}
            <ul className="plain-list">
              {p.versions.map((v) => (
                <li className="history-row" key={v.id}>
                  <div className="sub">
                    {v.origin === 'chat' ? t('history.chat') : t('history.app')} · {formatWhen(v.edited_at)}
                    {!v.applied && ` · ${t('history.notApplied')}`}
                    {v.text === current && ` · ${t('history.current')}`}
                  </div>
                  <pre className="history-text">{v.text}</pre>
                  {v.text !== current && (
                    <button
                      className="btn small"
                      onClick={() => {
                        edit.mutate({ note, partId: p.part_id, text: v.text })
                        onClose()
                      }}
                      aria-label={`${t('history.restore')}: ${formatWhen(v.edited_at)}`}
                    >
                      {t('history.restore')}
                    </button>
                  )}
                </li>
              ))}
            </ul>
          </section>
        )
      })}
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
