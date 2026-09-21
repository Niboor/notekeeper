import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, unwrap, unwrapEmpty, type Schemas } from '../../api/client'

export type Reminder = Schemas['Reminder']
export type UpcomingReminder = Schemas['UpcomingReminder']
export type AppNotification = Schemas['Notification']

export function useUpcomingReminders() {
  return useQuery({ queryKey: ['reminders'], queryFn: async () => unwrap(await api.GET('/api/v1/reminders')).items })
}

export function useNotifications() {
  return useQuery({ queryKey: ['notifications'], queryFn: async () => unwrap(await api.GET('/api/v1/notifications')) })
}

/** Reminders show on notes, in the upcoming list and in the bell, so a change refreshes all of them. */
function useRefresh() {
  const qc = useQueryClient()
  return () => {
    void qc.invalidateQueries({ queryKey: ['reminders'] })
    void qc.invalidateQueries({ queryKey: ['inbox'] })
    void qc.invalidateQueries({ queryKey: ['board'] })
    void qc.invalidateQueries({ queryKey: ['trash'] })
    void qc.invalidateQueries({ queryKey: ['note'] })
  }
}

export function useCreateReminder(noteId: string) {
  const refresh = useRefresh()
  return useMutation({
    mutationFn: async (v: { dueAt: Date; rrule?: string }) =>
      unwrap(await api.POST('/api/v1/notes/{id}/reminders', { params: { path: { id: noteId } }, body: { due_at: v.dueAt.toISOString(), rrule: v.rrule } })),
    onSuccess: refresh,
  })
}

export function useSnoozeReminder() {
  const refresh = useRefresh()
  return useMutation({
    mutationFn: async (v: { id: string; until: Date }) =>
      unwrap(await api.POST('/api/v1/reminders/{id}/snooze', { params: { path: { id: v.id } }, body: { until: v.until.toISOString() } })),
    onSuccess: refresh,
  })
}

export function useCompleteReminder() {
  const refresh = useRefresh()
  return useMutation({
    mutationFn: async (id: string) => unwrap(await api.POST('/api/v1/reminders/{id}/done', { params: { path: { id } } })),
    onSuccess: refresh,
  })
}

export function useDeleteReminder() {
  const refresh = useRefresh()
  return useMutation({
    mutationFn: async (id: string) => unwrapEmpty(await api.DELETE('/api/v1/reminders/{id}', { params: { path: { id } } })),
    onSuccess: refresh,
  })
}

export function useMarkNotificationsRead() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async () => unwrapEmpty(await api.POST('/api/v1/notifications/read', { body: {} })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['notifications'] }),
  })
}

// ---- times -----------------------------------------------------------------------------------

/** The nth hour of a day in the browser's zone, `days` days from now. */
export function at(hour: number, days: number, from: Date = new Date()): Date {
  const d = new Date(from)
  d.setDate(d.getDate() + days)
  d.setHours(hour, 0, 0, 0)
  return d
}

/** A moment `minutes` from now. */
export function inMinutes(minutes: number): Date {
  return new Date(Date.now() + minutes * 60_000)
}

export function isPast(d: Date): boolean {
  return d.getTime() <= Date.now()
}

export function nextMonday(from: Date = new Date()): Date {
  const delta = ((8 - from.getDay()) % 7) || 7
  return at(9, delta, from)
}

export function quickOptions(now: Date = new Date()): { key: 'hour' | 'evening' | 'tomorrow' | 'monday'; when: Date }[] {
  const opts: { key: 'hour' | 'evening' | 'tomorrow' | 'monday'; when: Date }[] = [{ key: 'hour', when: new Date(now.getTime() + 3600_000) }]
  const evening = at(18, 0, now)
  if (evening.getTime() > now.getTime() + 15 * 60_000) opts.push({ key: 'evening', when: evening })
  opts.push({ key: 'tomorrow', when: at(9, 1, now) }, { key: 'monday', when: nextMonday(now) })
  return opts
}

/** "2026-03-11T14:30" for a datetime-local field, in the browser's zone. */
export function toLocalInput(d: Date): string {
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`
}

/** How many calendar days `d` is from `from` (0 for today, 1 for tomorrow), in the browser's zone. */
export function daysAway(d: Date, from: Date = new Date()): number {
  const day = (x: Date) => Date.UTC(x.getFullYear(), x.getMonth(), x.getDate())
  return Math.round((day(d) - day(from)) / 86_400_000)
}

export function repeatWord(rrule: string | null | undefined): 'daily' | 'weekly' | 'monthly' | 'other' | null {
  if (!rrule) return null
  const freq = /FREQ=(\w+)/.exec(rrule)?.[1]
  const plain = !/INTERVAL=(?!1(;|$))|BYDAY/.test(rrule)
  if (plain && freq === 'DAILY') return 'daily'
  if (plain && freq === 'WEEKLY') return 'weekly'
  if (plain && freq === 'MONTHLY') return 'monthly'
  return 'other'
}
