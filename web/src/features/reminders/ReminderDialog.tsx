import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { ApiError } from '../../api/client'
import { t, type MessageKey } from '../../i18n'
import { formatWhen } from '../share/ShareDialog'
import type { Note } from '../types'
import {
  at, daysAway, inMinutes, isPast, quickOptions, repeatWord, toLocalInput, useCompleteReminder, useCreateReminder, useDeleteReminder, useSnoozeReminder, type Reminder,
} from './hooks'

const HOURS = Array.from({ length: 24 }, (_, i) => String(i).padStart(2, '0'))
const MINUTES = Array.from({ length: 12 }, (_, i) => String(i * 5).padStart(2, '0'))
const DAY_SHORTCUTS = [
  { key: 'today', days: 0 },
  { key: 'tomorrow', days: 1 },
  { key: 'week', days: 7 },
] as const
const TIME_SHORTCUTS = [
  ['09', '00'],
  ['12', '00'],
  ['15', '00'],
  ['18', '00'],
] as const
const dayOf = (days: number) => toLocalInput(at(9, days)).slice(0, 10)

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
  // A custom time is a date and a time of day, chosen apart: emptying the date field keeps the time.
  const [start] = useState(() => toLocalInput(at(9, 1)))
  const [day, setDay] = useState(start.slice(0, 10))
  const [hour, setHour] = useState(start.slice(11, 13))
  const [minute, setMinute] = useState(start.slice(14, 16))
  const [repeat, setRepeat] = useState<keyof typeof RULES>('none')
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    const d = ref.current
    if (d && !d.open) {
      if (typeof d.showModal === 'function') d.showModal()
      else d.setAttribute('open', '')
    }
  }, [])

  const chosen = day ? new Date(`${day}T${hour}:${minute}`) : null
  const past = chosen !== null && isPast(chosen)
  const away = chosen ? daysAway(chosen) : 0
  const preview = chosen
    ? past
      ? t('remind.past')
      : t('remind.preview', {
          when: chosen.toLocaleString(undefined, { weekday: 'long', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }),
          away: away === 0 ? t('remind.in.today') : away === 1 ? t('remind.in.tomorrow') : t('remind.in.days', { n: String(away) }),
        })
    : ''

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
        <div className="when" role="group" aria-label={t('remind.custom')}>
          <input type="date" value={day} onChange={(e) => setDay(e.target.value)} aria-label={t('remind.date')} />
          <span className="time">
            <select value={hour} onChange={(e) => setHour(e.target.value)} aria-label={t('remind.hour')}>
              {HOURS.map((h) => (
                <option key={h}>{h}</option>
              ))}
            </select>
            <span aria-hidden="true">:</span>
            <select value={minute} onChange={(e) => setMinute(e.target.value)} aria-label={t('remind.minute')}>
              {MINUTES.map((m) => (
                <option key={m}>{m}</option>
              ))}
            </select>
          </span>
        </div>
        <div className="shortcuts" role="group" aria-label={t('remind.days')}>
          {DAY_SHORTCUTS.map((d) => (
            <button key={d.key} type="button" className="btn small" aria-pressed={day === dayOf(d.days)} onClick={() => setDay(dayOf(d.days))}>
              {t(`remind.day.${d.key}` as MessageKey)}
            </button>
          ))}
        </div>
        <div className="shortcuts" role="group" aria-label={t('remind.times')}>
          {TIME_SHORTCUTS.map(([h, m]) => (
            <button key={h} type="button" className="btn small" aria-pressed={hour === h && minute === m} onClick={() => (setHour(h), setMinute(m))}>
              {Number(h)}:{m}
            </button>
          ))}
        </div>
        <p className={`when-preview${past ? ' is-past' : ''}`} aria-live="polite">
          {preview}
        </p>
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
        <button className="btn primary" onClick={() => chosen && add(chosen)} disabled={!chosen || past}>
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
