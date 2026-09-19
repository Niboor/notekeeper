const rtf = new Intl.RelativeTimeFormat('en', { numeric: 'auto' })

/** "just now", "2 minutes ago", "yesterday", or a date for anything older than a week. */
export function timeAgo(iso: string, now: number = Date.now()): string {
  const then = new Date(iso).getTime()
  const seconds = Math.round((then - now) / 1000)
  const abs = Math.abs(seconds)
  if (abs < 45) return 'just now'
  if (abs < 3600) return rtf.format(Math.round(seconds / 60), 'minute')
  if (abs < 86400) return rtf.format(Math.round(seconds / 3600), 'hour')
  if (abs < 7 * 86400) return rtf.format(Math.round(seconds / 86400), 'day')
  return new Date(then).toLocaleDateString('en', { day: 'numeric', month: 'short', year: abs > 300 * 86400 ? 'numeric' : undefined })
}
