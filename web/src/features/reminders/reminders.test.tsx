import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fakeFetch, renderApp } from '../../test/helpers'
import type { Note } from '../types'
import { NotificationsBell } from './NotificationsBell'
import { ReminderDialog } from './ReminderDialog'
import { at, nextMonday, quickOptions, repeatWord, toLocalInput } from './hooks'

afterEach(() => vi.unstubAllGlobals())

// CORE-R2, WEB-18: quick options are computed in the browser's zone and only offer times ahead.
describe('quick times', () => {
  it('offers the evening only while it is still ahead, and always tomorrow and Monday', () => {
    const morning = new Date(2026, 2, 11, 8, 0) // Wednesday
    const keys = (d: Date) => quickOptions(d).map((q) => q.key)
    expect(keys(morning)).toEqual(['hour', 'evening', 'tomorrow', 'monday'])
    expect(keys(new Date(2026, 2, 11, 17, 55))).toEqual(['hour', 'tomorrow', 'monday'])
    const opts = quickOptions(morning)
    expect(opts.find((q) => q.key === 'tomorrow')!.when).toEqual(new Date(2026, 2, 12, 9, 0))
    expect(opts.find((q) => q.key === 'monday')!.when).toEqual(new Date(2026, 2, 16, 9, 0))
    expect(nextMonday(new Date(2026, 2, 16, 8, 0))).toEqual(new Date(2026, 2, 23, 9, 0)) // on a Monday: the next one
    expect(at(9, 1, new Date(2026, 2, 31, 12, 0))).toEqual(new Date(2026, 3, 1, 9, 0)) // month boundary
    expect(toLocalInput(new Date(2026, 0, 5, 7, 3))).toBe('2026-01-05T07:03')
  })

  it('names simple repeats and calls the rest "repeats"', () => {
    expect(repeatWord('FREQ=DAILY')).toBe('daily')
    expect(repeatWord('FREQ=WEEKLY')).toBe('weekly')
    expect(repeatWord('FREQ=MONTHLY;INTERVAL=1')).toBe('monthly')
    expect(repeatWord('FREQ=WEEKLY;BYDAY=MO,WE')).toBe('other')
    expect(repeatWord('FREQ=DAILY;INTERVAL=2')).toBe('other')
    expect(repeatWord(null)).toBeNull()
  })
})

const note = (over: Partial<Note> = {}): Note =>
  ({ id: 'n1', state: 'active', created_at: '2026-03-01T10:00:00Z', updated_at: '2026-03-01T10:00:00Z', version: 1, parts: [], ...over }) as Note

describe('ReminderDialog', () => {
  it('sets a reminder from a quick option, and repeats when asked', async () => {
    const bodies: unknown[] = []
    vi.stubGlobal(
      'fetch',
      fakeFetch([
        {
          method: 'POST',
          path: '/api/v1/notes/n1/reminders',
          status: 201,
          handler: async (req) => {
            bodies.push(await req.json())
            return { id: 'r1', note_id: 'n1', due_at: '2030-01-01T09:00:00Z', tz: 'UTC', state: 'pending', version: 1 }
          },
        },
      ]),
    )
    renderApp(<ReminderDialog note={note()} onClose={() => undefined} />)
    const user = userEvent.setup()
    expect(screen.getByText('No reminders on this note.')).toBeInTheDocument()
    await user.selectOptions(screen.getByLabelText('Repeat'), 'weekly')
    await user.click(screen.getByRole('button', { name: 'Tomorrow 9:00' }))
    await vi.waitFor(() => expect(bodies).toHaveLength(1))
    const sent = bodies[0] as { due_at: string; rrule: string }
    expect(sent.rrule).toBe('FREQ=WEEKLY')
    expect(new Date(sent.due_at).getTime()).toBeGreaterThan(Date.now())
  })

  it('refuses a time in the past without asking the server', async () => {
    const f = fakeFetch([])
    vi.stubGlobal('fetch', f)
    renderApp(<ReminderDialog note={note()} onClose={() => undefined} />)
    const user = userEvent.setup()
    const field = screen.getByLabelText('Or pick a time')
    await user.clear(field)
    await user.type(field, '2020-01-01T09:00')
    await user.click(screen.getByRole('button', { name: 'Set reminder' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('future')
    expect(f.calls.filter((c) => c.method === 'POST')).toHaveLength(0)
  })

  it('lists existing reminders with snooze, done and clear', async () => {
    const calls: string[] = []
    vi.stubGlobal(
      'fetch',
      fakeFetch([
        { method: 'POST', path: '/api/v1/reminders/r1/done', handler: () => { calls.push('done'); return { id: 'r1', note_id: 'n1', due_at: '2030-01-01T09:00:00Z', tz: 'UTC', state: 'done', version: 2 } } },
        { method: 'DELETE', path: '/api/v1/reminders/r1', status: 204, handler: () => { calls.push('clear'); return undefined } },
      ]),
    )
    const r = { id: 'r1', note_id: 'n1', due_at: '2030-01-01T09:00:00Z', tz: 'UTC', state: 'pending' as const, version: 1, rrule: 'FREQ=DAILY' }
    renderApp(<ReminderDialog note={note({ reminders: [r] })} onClose={() => undefined} />)
    const user = userEvent.setup()
    expect(screen.getByText(/every day/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Mark done' }))
    await user.click(screen.getByRole('button', { name: 'Clear' }))
    await vi.waitFor(() => expect(calls).toEqual(['done', 'clear']))
  })
})

describe('NotificationsBell', () => {
  it('shows the unread count, links a reminder to its note, and marks everything read', async () => {
    let read = false
    vi.stubGlobal(
      'fetch',
      fakeFetch([
        {
          path: '/api/v1/notifications',
          handler: () => ({
            unread: read ? 0 : 2,
            items: [
              { id: 'a', kind: 'reminder', payload: { excerpt: 'call the dentist', note_id: 'n7' }, created_at: '2026-03-01T10:00:00Z' },
              { id: 'b', kind: 'security', payload: { text: '🔐 New sign-in' }, created_at: '2026-03-01T09:00:00Z' },
            ],
          }),
        },
        { method: 'POST', path: '/api/v1/notifications/read', status: 204, handler: () => { read = true; return undefined } },
      ]),
    )
    renderApp(<NotificationsBell />)
    const user = userEvent.setup()
    const bell = await screen.findByRole('button', { name: 'Notifications, 2 unread' })
    await user.click(bell)
    expect(screen.getByRole('link', { name: /call the dentist/ })).toHaveAttribute('href', '/notes/n7')
    expect(screen.getByText('🔐 New sign-in')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Mark all as read' }))
    expect(await screen.findByRole('button', { name: 'Notifications' })).toBeInTheDocument()
  })
})
