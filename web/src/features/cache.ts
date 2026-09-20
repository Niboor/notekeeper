// Optimistic updates of the cached views (docs/design/07 section 4). These functions are pure:
// they take cached data and return new data, so the rules for where a note lands are tested
// without a browser. The mutation hooks apply them with `setQueryData` before the server answers
// and roll back when it refuses.
import type { InfiniteData } from '@tanstack/react-query'
import { INBOX, type Board, type Note, type NotePage } from './types'

export type InboxData = InfiniteData<NotePage, string | undefined>

/** Removes a note from the loaded Inbox pages. */
export function removeFromInbox(data: InboxData | undefined, id: string): InboxData | undefined {
  if (!data) return data
  let removed = false
  const pages = data.pages.map((p) => {
    const items = p.items.filter((n) => n.id !== id)
    if (items.length !== p.items.length) removed = true
    return { ...p, items }
  })
  if (!removed) return data
  const first = pages[0]
  if (first?.total !== undefined && first.total !== null) pages[0] = { ...first, total: first.total - 1 }
  return { ...data, pages }
}

/** Inserts a note into the Inbox in creation-time order (newest first): the Inbox is never manually ordered. */
export function insertIntoInbox(data: InboxData | undefined, note: Note): InboxData | undefined {
  if (!data || data.pages.length === 0) return data
  const pages = data.pages.map((p) => ({ ...p, items: [...p.items] }))
  const first = pages[0]!
  // Compared as times: the server writes microseconds (or none, when they are zero), the client milliseconds,
  // and as strings 10:00:00.5Z sorts before 10:00:00Z.
  const created = Date.parse(note.created_at)
  const at = first.items.findIndex((n) => Date.parse(n.created_at) < created)
  if (at === -1 && pages.length > 1) return data // older than everything loaded: it belongs on a later page
  first.items.splice(at === -1 ? first.items.length : at, 0, { ...note, category_id: null })
  if (first.total !== undefined && first.total !== null) first.total += 1
  return { ...data, pages }
}

/** Where a note sits on a board. */
export function findOnBoard(board: Board, id: string): { category: string; index: number } | null {
  for (const c of board.categories) {
    const index = c.notes.findIndex((n) => n.id === id)
    if (index >= 0) return { category: c.category.id, index }
  }
  return null
}

/** Removes a note from every column of a board. */
export function removeFromBoard(board: Board, id: string): Board {
  if (!findOnBoard(board, id)) return board
  return {
    ...board,
    categories: board.categories.map((c) => {
      const notes = c.notes.filter((n) => n.id !== id)
      return notes.length === c.notes.length ? c : { ...c, notes, total: c.total - 1 }
    }),
  }
}

/** Inserts a note into a column at an index (clamped), updating the column's count. */
export function insertIntoBoard(board: Board, categoryId: string, note: Note, index: number): Board {
  if (!board.categories.some((c) => c.category.id === categoryId)) return board
  return {
    ...board,
    categories: board.categories.map((c) => {
      if (c.category.id !== categoryId) return c
      const notes = [...c.notes]
      notes.splice(Math.max(0, Math.min(index, notes.length)), 0, { ...note, category_id: categoryId })
      return { ...c, notes, total: c.total + 1 }
    }),
  }
}

/** Adjusts the Inbox count shown next to a board. */
export function withInboxTotal(board: Board, delta: number): Board {
  return { ...board, inbox_total: Math.max(0, board.inbox_total + delta) }
}

/** Replaces a note wherever it appears on a board (after an edit) without moving it. */
export function replaceOnBoard(board: Board, note: Note): Board {
  if (!findOnBoard(board, note.id)) return board
  return {
    ...board,
    categories: board.categories.map((c) => ({ ...c, notes: c.notes.map((n) => (n.id === note.id ? note : n)) })),
  }
}

export function replaceInInbox(data: InboxData | undefined, note: Note): InboxData | undefined {
  if (!data) return data
  return { ...data, pages: data.pages.map((p) => ({ ...p, items: p.items.map((n) => (n.id === note.id ? note : n)) })) }
}

/** Neighbours of an index within a list of ids: the note it should follow and the one it should precede. */
export function neighbours(ids: readonly string[], index: number): { afterId: string | null; beforeId: string | null } {
  return { afterId: ids[index - 1] ?? null, beforeId: ids[index + 1] ?? null }
}

export { INBOX }
