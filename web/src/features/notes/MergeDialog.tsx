import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { plainExcerpt } from '../../components/markdown'
import { t } from '../../i18n'
import { useMergeNotes } from '../hooks'
import type { Note } from '../types'

function label(n: Note): string {
  const text = n.parts.find((p) => p.kind === 'text')?.text ?? n.parts.find((p) => p.attachment)?.attachment?.filename ?? '…'
  return plainExcerpt(text) || '…'
}

/** Pick another note of the same column to merge into this one (WEB-15, CORE-N14). */
export function MergeDialog({ note, candidates, onClose }: { note: Note; candidates: Note[]; onClose: () => void }) {
  const merge = useMergeNotes()
  const ref = useRef<HTMLDialogElement>(null)
  const [source, setSource] = useState(candidates[0]?.id ?? '')
  const [failed, setFailed] = useState(false)
  useEffect(() => {
    const d = ref.current
    if (d && !d.open) {
      if (typeof d.showModal === 'function') d.showModal()
      else d.setAttribute('open', '')
    }
  }, [])
  const stop = (e: React.SyntheticEvent) => e.stopPropagation()
  return createPortal(
    <dialog ref={ref} className="dialog" aria-labelledby="merge-h" onClose={onClose} onCancel={onClose} onKeyDown={stop} onKeyUp={stop} onPointerDown={stop} onMouseDown={stop} onTouchStart={stop} onClick={stop}>
      <h2 id="merge-h">{t('merge.title')}</h2>
      {candidates.length === 0 ? (
        <p>{t('merge.none')}</p>
      ) : (
        <>
          <p>{t('merge.lead')}</p>
          <select aria-label={t('merge.choose')} value={source} onChange={(e) => setSource(e.target.value)}>
            {candidates.map((c) => (
              <option key={c.id} value={c.id}>
                {label(c)}
              </option>
            ))}
          </select>
        </>
      )}
      {failed && (
        <p className="form-error" role="alert">
          {t('merge.error')}
        </p>
      )}
      <div className="composer-bar">
        <span className="grow" />
        <button className="btn" onClick={onClose}>
          {t('share.close')}
        </button>
        {candidates.length > 0 && (
          <button className="btn primary" disabled={!source || merge.isPending} onClick={() => merge.mutate({ target: note.id, source }, { onSuccess: onClose, onError: () => setFailed(true) })}>
            {t('merge.confirm')}
          </button>
        )}
      </div>
    </dialog>,
    document.body,
  )
}
