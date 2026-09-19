import { describe, expect, it } from 'vitest'
import { timeAgo } from './time'

describe('timeAgo', () => {
  const now = new Date('2026-09-20T12:00:00Z').getTime()
  const ago = (s: number) => new Date(now - s * 1000).toISOString()
  it('reads naturally', () => {
    expect(timeAgo(ago(10), now)).toBe('just now')
    expect(timeAgo(ago(120), now)).toBe('2 minutes ago')
    expect(timeAgo(ago(3 * 3600), now)).toBe('3 hours ago')
    expect(timeAgo(ago(86400), now)).toBe('yesterday')
    expect(timeAgo(ago(3 * 86400), now)).toBe('3 days ago')
  })
  it('falls back to a date after a week', () => {
    expect(timeAgo(ago(20 * 86400), now)).toMatch(/Aug/)
  })
})
