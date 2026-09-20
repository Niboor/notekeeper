import { Link } from 'react-router'
import { t } from '../../i18n'
import { formatWhen } from '../share/ShareDialog'
import { at, repeatWord, useCompleteReminder, useDeleteReminder, useSnoozeReminder, useUpcomingReminders } from './hooks'
import type { MessageKey } from '../../i18n'

/** Every armed reminder, soonest first, with the quick actions (CORE-R9, WEB-18). */
export function RemindersPage() {
  const list = useUpcomingReminders()
  const snooze = useSnoozeReminder()
  const complete = useCompleteReminder()
  const clear = useDeleteReminder()
  return (
    <div className="page-body">
      <div className="page-narrow">
        <h1>{t('remind.pageTitle')}</h1>
        {list.data?.length === 0 && <p>{t('remind.pageNone')}</p>}
        {list.data?.map((r) => (
          <div className="row" key={r.id}>
            <div className="grow">
              <Link to={`/notes/${r.note_id}`}>{r.excerpt || t('remind.open')}</Link>
              <div className="sub">
                {t('remind.due', { when: formatWhen(r.due_at) })}
                {repeatWord(r.rrule) ? ` · ${t(`remind.repeats.${repeatWord(r.rrule)}` as MessageKey)}` : ''}
              </div>
            </div>
            <div className="row-actions">
              <button className="btn small" onClick={() => snooze.mutate({ id: r.id, until: at(9, 1) })} aria-label={`${t('remind.snooze')} ${t('remind.snooze.tomorrow')}: ${r.excerpt}`}>
                {t('remind.snooze')} · {t('remind.snooze.tomorrow')}
              </button>
              <button className="btn small" onClick={() => complete.mutate(r.id)} aria-label={`${t('remind.markDone')}: ${r.excerpt}`}>
                {t('remind.markDone')}
              </button>
              <button className="btn small" onClick={() => clear.mutate(r.id)} aria-label={`${t('remind.clear')}: ${r.excerpt}`}>
                {t('remind.clear')}
              </button>
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}
