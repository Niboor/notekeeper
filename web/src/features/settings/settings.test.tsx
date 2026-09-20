import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cancelScheduledRefresh } from '../../api/session'
import { fakeFetch, me, renderApp } from '../../test/helpers'
import { SettingsPage } from './SettingsPage'

afterEach(() => {
  cancelScheduledRefresh()
  vi.unstubAllGlobals()
})

// WEB-12, AUTH-U11, CORE-R3: choose which chat gets reminders, which notices are muted, the time
// zone, and the grouping window.
describe('SettingsPage', () => {
  it('saves reminder targets, muted notices, the time zone and the grouping window', async () => {
    const saved: Record<string, unknown>[] = []
    let target: unknown
    vi.stubGlobal(
      'fetch',
      fakeFetch([
        { path: '/api/v1/me', body: { ...me, timezone: 'Europe/Brussels', settings: { tz_auto: true } } },
        { path: '/api/v1/me/identities', body: { items: [{ id: 'i1', bot_instance_name: 'matrix', bot_type: 'matrix', external_user_id: '@alice:x', reminder_target: true, linked_at: '2026-01-01T00:00:00Z' }] } },
        { path: '/api/v1/bot-instances', body: { items: [] } },
        { path: '/api/v1/me/sessions', body: { items: [] } },
        { path: '/api/v1/share-links', body: { items: [], enabled: true, max_lifetime_seconds: 2592000 } },
        {
          method: 'PATCH',
          path: '/api/v1/me',
          handler: async (req) => {
            const body = (await req.json()) as Record<string, unknown>
            saved.push(body)
            return { ...me, ...body }
          },
        },
        {
          method: 'PATCH',
          path: '/api/v1/me/identities/i1',
          handler: async (req) => {
            target = await req.json()
            return { id: 'i1', bot_instance_name: 'matrix', bot_type: 'matrix', external_user_id: '@alice:x', reminder_target: false, linked_at: '2026-01-01T00:00:00Z' }
          },
        },
      ]),
    )
    renderApp(<SettingsPage />)
    const user = userEvent.setup()

    await user.click(await screen.findByRole('checkbox', { name: /Send reminders here/ }))
    await vi.waitFor(() => expect(target).toEqual({ reminder_target: false }))

    // Notices: sign-ins are on; muting them keeps the other settings.
    const signIns = await screen.findByRole('checkbox', { name: 'Tell me about new sign-ins' })
    expect(signIns).toBeChecked()
    await user.click(signIns)
    await vi.waitFor(() => expect(saved[0]).toMatchObject({ settings: { tz_auto: true, muted_notices: ['session'] } }))
    expect(screen.queryByRole('checkbox', { name: /password/i })).toBeNull() // the others cannot be turned off

    await user.selectOptions(screen.getByRole('combobox', { name: 'Time zone' }), 'Europe/Lisbon')
    await vi.waitFor(() => expect(saved[1]).toMatchObject({ timezone: 'Europe/Lisbon' }))

    await user.selectOptions(screen.getByRole('combobox', { name: 'Grouping window' }), '120')
    await vi.waitFor(() => expect(saved[2]).toMatchObject({ settings: { grouping_window_seconds: 120 } }))
  })
})
