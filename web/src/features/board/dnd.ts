// The pure part of drag and drop (docs/design/07 section 11): how a drag rearranges the lanes,
// and what the screen reader is told. Kept apart from dnd-kit so it can be tested without a browser.
import { t } from '../../i18n'
import { INBOX } from '../types'

/** Lane id → the ids of the notes in it, in display order. */
export type Arrangement = Record<string, string[]>

export const laneDropId = (lane: string) => `lane:${lane}`
export const pageDropId = (page: string) => `page:${page}`
export const isLaneDrop = (id: string) => id.startsWith('lane:')
export const isPageDrop = (id: string) => id.startsWith('page:')
export const laneOf = (dropId: string) => dropId.slice('lane:'.length)
export const pageOf = (dropId: string) => dropId.slice('page:'.length)

export function findLane(a: Arrangement, note: string): string | undefined {
  return Object.keys(a).find((lane) => a[lane]?.includes(note))
}

/** Moves a note into a lane at an index, removing it from wherever it was (also when it came from another page). */
export function moveAcross(a: Arrangement, note: string, toLane: string, index: number): Arrangement {
  const out: Arrangement = {}
  for (const [lane, ids] of Object.entries(a)) out[lane] = ids.filter((id) => id !== note)
  const target = out[toLane]
  if (!target) return a
  target.splice(Math.max(0, Math.min(index, target.length)), 0, note)
  return out
}

/** Reorders a lane so that `note` sits where `over` is. */
export function reorder(a: Arrangement, lane: string, note: string, over: string): Arrangement {
  const ids = a[lane]
  if (!ids) return a
  const from = ids.indexOf(note)
  const to = ids.indexOf(over)
  if (from < 0 || to < 0 || from === to) return a
  const next = [...ids]
  next.splice(from, 1)
  next.splice(to, 0, note)
  return { ...a, [lane]: next }
}

/** The index at which a note dragged over `over` should be inserted: after it when the pointer is in its lower half. */
export function insertIndex(ids: readonly string[], over: string, below: boolean): number {
  const i = ids.indexOf(over)
  return i < 0 ? ids.length : i + (below ? 1 : 0)
}

export interface Placement {
  lane: string
  index: number
  ids: readonly string[]
}

/** Where a note ended up. */
export function placementOf(a: Arrangement, note: string): Placement | null {
  const lane = findLane(a, note)
  if (lane === undefined) return null
  const ids = a[lane]!
  return { lane, index: ids.indexOf(note), ids }
}

/** True when a drop leaves the note exactly where it started. */
export function isUnchanged(before: Placement | null, after: Placement | null): boolean {
  return !!before && !!after && before.lane === after.lane && before.index === after.index
}

// ---- announcements (WEB-N4) ---------------------------------------------------------------------

export function announceMoved(kind: 'over' | 'drop', laneName: string, index: number, total: number): string {
  return t(kind === 'drop' ? 'dnd.dropped' : 'dnd.movedTo', { lane: laneName, pos: index + 1, total })
}

export const laneTitle = (lane: string, names: Record<string, string>) => (lane === INBOX ? t('inbox.title') : (names[lane] ?? ''))
