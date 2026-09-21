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

/**
 * Rebuilds a drag arrangement after the lanes on screen changed under it (the drag went to another
 * page, or a lane appeared or vanished). Server lanes win; the dragged note keeps its place if its
 * lane still exists and is otherwise in no lane until it is dragged over one.
 */
export function rebase(current: Arrangement, served: Arrangement, note: string): Arrangement {
  const sameLanes = Object.keys(served).length === Object.keys(current).length && Object.keys(served).every((l) => l in current)
  if (sameLanes) return current
  const out: Arrangement = {}
  for (const [lane, ids] of Object.entries(served)) out[lane] = ids.filter((id) => id !== note)
  const lane = findLane(current, note)
  if (lane !== undefined && out[lane]) out[lane].splice(Math.min(current[lane]!.indexOf(note), out[lane].length), 0, note)
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

/** One keyboard step for a lifted note: the arrows move it within a lane and to the neighbouring lane. */
export function keyboardStep(a: Arrangement, laneOrder: readonly string[], note: string, key: string): Arrangement | null {
  const lane = findLane(a, note)
  if (lane === undefined) return null
  const ids = a[lane]!
  const index = ids.indexOf(note)
  switch (key) {
    case 'ArrowUp':
      return index > 0 ? moveAcross(a, note, lane, index - 1) : null
    case 'ArrowDown':
      return index < ids.length - 1 ? moveAcross(a, note, lane, index + 1) : null
    case 'ArrowLeft':
    case 'ArrowRight': {
      const to = laneOrder[laneOrder.indexOf(lane) + (key === 'ArrowRight' ? 1 : -1)]
      return to === undefined ? null : moveAcross(a, note, to, Math.min(index, a[to]?.length ?? 0))
    }
    default:
      return null
  }
}

// ---- announcements (WEB-N4) ---------------------------------------------------------------------

export function announceMoved(kind: 'over' | 'drop', laneName: string, index: number, total: number): string {
  return t(kind === 'drop' ? 'dnd.dropped' : 'dnd.movedTo', { lane: laneName, pos: index + 1, total })
}

export const laneTitle = (lane: string, names: Record<string, string>) => (lane === INBOX ? t('inbox.title') : (names[lane] ?? ''))

// ---- columns ------------------------------------------------------------------------------------

/** A column is dragged by its header; it can be dropped on another column (before or after it) or on a page tab. */
export const colDragId = (column: string) => `col:${column}`
export const colZoneId = (column: string) => `colzone:${column}`
export const isColDrag = (id: string) => id.startsWith('col:')
export const isColZone = (id: string) => id.startsWith('colzone:')
export const colOf = (id: string) => id.slice(id.indexOf(':') + 1)

export interface ColumnDrop {
  /** The index among the columns of the page it lands on, not counting the moved column itself. */
  index: number
  afterId: string | null
  beforeId: string | null
}

/** The place at the insertion index `index` among `others` (the columns of the page, without the moved one). */
export function columnPlace(others: readonly string[], index: number): ColumnDrop {
  const i = Math.max(0, Math.min(index, others.length))
  return { index: i, afterId: others[i - 1] ?? null, beforeId: others[i] ?? null }
}

/** Where a column dropped on the left half (`after` false) or right half of the column `over` lands. */
export function columnDrop(others: readonly string[], over: string, after: boolean): ColumnDrop {
  const i = others.indexOf(over)
  return columnPlace(others, i < 0 ? others.length : i + (after ? 1 : 0))
}

/** The column ids of a page in display order, without `column`. */
export const withoutColumn = (order: readonly string[], column: string) => order.filter((id) => id !== column)

/** True when a drop puts the column where it already is. */
export function columnUnchanged(originPage: string, originIndex: number, page: string, drop: ColumnDrop): boolean {
  return originPage === page && originIndex === drop.index
}
