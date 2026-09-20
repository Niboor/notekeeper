import { screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cancelScheduledRefresh } from '../api/session'
import { fakeFetch, me, renderApp } from '../test/helpers'
import { useAdoptBrowserTimezone } from './useAdoptBrowserTimezone'

afterEach(() => {
  cancelScheduledRefresh()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function Probe() {
  useAdoptBrowserTimezone()
  return <p>ready</p>
}

function browserZone(zone: string) {
  const real = Intl.DateTimeFormat
  vi.spyOn(Intl, 'DateTimeFormat').mockImplementation(((...args: ConstructorParameters<typeof Intl.DateTimeFormat>) => {
    const f = new real(...args)
    f.resolvedOptions = () => ({ ...real.prototype.resolvedOptions.call(f), timeZone: zone })
    return f
  }) as unknown as typeof Intl.DateTimeFormat)
}

// CORE-R2: a new account adopts the browser's time zone once, at first use.
describe('adopting the browser time zone', () => {
  it('sends the browser zone once and marks it, even when the answer changes the profile', async () => {
    browserZone('Europe/Brussels')
    const bodies: Record<string, unknown>[] = []
    const f = fakeFetch([
      { path: '/api/v1/me', body: { ...me, timezone: 'UTC', settings: {} } },
      {
        method: 'PATCH',
        path: '/api/v1/me',
        handler: async (req) => {
          const body = (await req.json()) as Record<string, unknown>
          bodies.push(body)
          return { ...me, timezone: body.timezone, settings: body.settings }
        },
      },
    ])
    vi.stubGlobal('fetch', f)
    renderApp(<Probe />)
    await screen.findByText('ready')
    await vi.waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({ timezone: 'Europe/Brussels', settings: { tz_auto: true } })
    await new Promise((r) => setTimeout(r, 200)) // the updated profile must not trigger another request
    expect(bodies).toHaveLength(1)
  })

  it('does nothing once it has been done, or for an account that already has a zone', async () => {
    browserZone('Europe/Brussels')
    for (const profile of [
      { timezone: 'UTC', settings: { tz_auto: true } }, // chose UTC on purpose earlier
      { timezone: 'America/New_York', settings: {} },
    ]) {
      const f = fakeFetch([{ path: '/api/v1/me', body: { ...me, ...profile } }])
      vi.stubGlobal('fetch', f)
      const { unmount } = renderApp(<Probe />)
      await screen.findByText('ready')
      await new Promise((r) => setTimeout(r, 100))
      expect(f.calls.filter((c) => c.method === 'PATCH')).toHaveLength(0)
      unmount()
    }
  })
})
