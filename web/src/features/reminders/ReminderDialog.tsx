import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { ApiError } from '../../api/client'
import { t, type MessageKey } from '../../i18n'
import { formatWhen } from '../share/ShareDialog'
import type { Note } from '../types'
import {
  at, inMinutes, isPast, quickOptions, repeatWord, toLocalInput, useCompleteReminder, useCreateReminder, useDeleteReminder, useSnoozeReminder, type Reminder,
} from './hooks'

const RULES = { none: undefined, daily: 'FREQ=DAILY', weekly: 'FREQ=WEEKLY', monthly: 'FREQ=MONTHLY' } as const

function status(r: Reminder): string {
  switch (r.state) {
    case 'pending':
      return t('remind.due', { when: formatWhen(r.due_at) })
    case 'fired':
      return t('remind.fired', { when: formatWhen(r.last_fired_at ?? r.due_at) })
    case 'done':
      return t('remind.done')
    case 'suspended':
      return t('remind.suspended')
    default:
      return t('remind.cancelled')
  }
}

/** Set, snooze, finish and clear the reminders of one note (WEB-18, CORE-R1, CORE-R8, CORE-R10). */
export function ReminderDialog({ note, onClose }: { note: Note; onClose: () => void }) {
  const create = useCreateReminder(note.id)
  const snooze = useSnoozeReminder()
  const complete = useCompleteReminder()
  const clear = useDeleteReminder()
  const ref = useRef<HTMLDialogElement>(null)
  const [custom, setCustom] = useState(() => toLocalInput(at(9, 1)))
  const [repeat, setRepeat] = useState<keyof typeof RULES>('none')
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    const d = ref.current
    if (d && !d.open) {
      if (typeof d.showModal === 'function') d.showModal()
      else d.setAttribute('open', '')
    }
  }, [])

  const add = (when: Date) => {
    setError(null)
    if (isPast(when)) {
      setError(t('remind.past'))
      return
    }
    create.mutate(
      { dueAt: when, rrule: RULES[repeat] },
      {
        onError: (e) => setError(e instanceof ApiError && e.code === 'reminder_limit' ? t('remind.limit') : e instanceof ApiError && e.code === 'reminder_in_past' ? t('remind.past') : t('remind.error')),
      },
    )
  }
  const stop = (e: React.SyntheticEvent) => e.stopPropagation()
  const list = note.reminders ?? []
  const snoozes = [
    { key: '10m', when: () => inMinutes(10) },
    { key: '1h', when: () => inMinutes(60) },
    { key: 'tomorrow', when: () => at(9, 1) },
  ] as const

  return createPortal(
    <dialog
      ref={ref}
      className="dialog"
      aria-labelledby="remind-h"
      onClose={onClose}
      onCancel={onClose}
      onKeyDown={stop}
      onKeyUp={stop}
      onPointerDown={stop}
      onMouseDown={stop}
      onTouchStart={stop}
      onClick={stop}
    >
      <h2 id="remind-h">{t('remind.title')}</h2>
      <p>{t('remind.lead')}</p>
      {list.length === 0 && <p>{t('remind.none')}</p>}
      <ul className="plain-list">
        {list.map((r) => (
          <li className="reminder-row" key={r.id}>
            <div className="grow">
              {status(r)}
              {repeatWord(r.rrule) && <span className="sub"> · {t(`remind.repeats.${repeatWord(r.rrule)}` as MessageKey)}</span>}
            </div>
            <div className="row-actions">
              {(r.state === 'pending' || r.state === 'fired') &&
                snoozes.map((s) => (
                  <button key={s.key} className="btn small" onClick={() => snooze.mutate({ id: r.id, until: s.when() })} aria-label={`${t('remind.snooze')} ${t(`remind.snooze.${s.key}` as MessageKey)}`}>
                    {t(`remind.snooze.${s.key}` as MessageKey)}
                  </button>
                ))}
              {(r.state === 'pending' || r.state === 'fired') && (
                <button className="btn small" onClick={() => complete.mutate(r.id)}>
                  {t('remind.markDone')}
                </button>
              )}
              <button className="btn small" onClick={() => clear.mutate(r.id)}>
                {t('remind.clear')}
              </button>
            </div>
          </li>
        ))}
      </ul>

      <h3>{t('remind.quick')}</h3>
      <div className="quick">
        {quickOptions().map((q) => (
          <button key={q.key} className="btn" onClick={() => add(q.when)}>
            {t(`remind.q.${q.key}` as MessageKey)}
          </button>
        ))}
      </div>
      <h3>{t('remind.custom')}</h3>
      <div className="remind-custom">
        <input type="datetime-local" value={custom} onChange={(e) => setCustom(e.target.value)} aria-label={t('remind.custom')} />
        <div className="repeat">
          <label htmlFor="remind-repeat" className="sub">
            {t('remind.repeat')}
          </label>
          <select id="remind-repeat" value={repeat} onChange={(e) => setRepeat(e.target.value as keyof typeof RULES)}>
            {(Object.keys(RULES) as (keyof typeof RULES)[]).map((k) => (
              <option key={k} value={k}>
                {t(`remind.repeat.${k}` as MessageKey)}
              </option>
            ))}
          </select>
        </div>
        <button className="btn primary" onClick={() => add(new Date(custom))} disabled={!custom}>
          {t('remind.set')}
        </button>
      </div>
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      <div className="composer-bar">
        <span className="grow" />
        <button className="btn" onClick={onClose}>
          {t('remind.close')}
        </button>
      </div>
    </dialog>,
    document.body,
  )
}
