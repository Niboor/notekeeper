import { describe, expect, it } from 'vitest'
import { findOnBoard, insertIntoBoard, insertIntoInbox, neighbours, removeFromBoard, removeFromInbox, replaceOnBoard, type InboxData } from './cache'
import type { Board, Note } from './types'

const note = (id: string, created = '2026-01-01T00:00:00Z', text = id): Note => ({
  id, state: 'active', category_id: null, created_at: created, updated_at: created, version: 1,
  parts: [{ id: id + 'p', kind: 'text', text, attach_reason: 'first', created_at: created }],
})

const inbox = (...notes: Note[]): InboxData => ({ pages: [{ items: notes, total: notes.length }], pageParams: [undefined] })

const board = (): Board => ({
  page: { id: 'p', name: 'P', version: 1 }, inbox_total: 2,
  categories: [
    { category: { id: 'a', page_id: 'p', name: 'A', version: 1 }, notes: [note('n1'), note('n2')], total: 2 },
    { category: { id: 'b', page_id: 'p', name: 'B', version: 1 }, notes: [], total: 0 },
  ],
})

describe('optimistic cache helpers', () => {
  it('moves a note between columns with the right counts', () => {
    let b = board()
    const moved = b.categories[0]!.notes[0]!
    b = removeFromBoard(b, 'n1')
    b = insertIntoBoard(b, 'b', moved, 0)
    expect(b.categories[0]!.notes.map((n) => n.id)).toEqual(['n2'])
    expect(b.categories[1]!.notes.map((n) => n.id)).toEqual(['n1'])
    expect([b.categories[0]!.total, b.categories[1]!.total]).toEqual([1, 1])
    expect(b.categories[1]!.notes[0]!.category_id).toBe('b')
  })

  it('clamps the index and ignores unknown columns', () => {
    const b = board()
    expect(insertIntoBoard(b, 'a', note('x'), 99).categories[0]!.notes.map((n) => n.id)).toEqual(['n1', 'n2', 'x'])
    expect(insertIntoBoard(b, 'a', note('x'), -5).categories[0]!.notes.map((n) => n.id)).toEqual(['x', 'n1', 'n2'])
    expect(insertIntoBoard(b, 'nope', note('x'), 0)).toBe(b)
  })

  it('leaves the board untouched when the note is not on it', () => {
    const b = board()
    expect(removeFromBoard(b, 'zzz')).toBe(b)
    expect(findOnBoard(b, 'n2')).toEqual({ category: 'a', index: 1 })
    expect(findOnBoard(b, 'zzz')).toBeNull()
  })

  it('keeps the Inbox in creation order, newest first (D1)', () => {
    const data = inbox(note('new', '2026-03-01T00:00:00Z'), note('old', '2026-01-01T00:00:00Z'))
    const out = insertIntoInbox(data, note('mid', '2026-02-01T00:00:00Z'))!
    expect(out.pages[0]!.items.map((n) => n.id)).toEqual(['new', 'mid', 'old'])
    expect(out.pages[0]!.total).toBe(3)
    expect(insertIntoInbox(data, note('newest', '2026-04-01T00:00:00Z'))!.pages[0]!.items[0]!.id).toBe('newest')
  })

  it('removes from the Inbox and lowers its count', () => {
    const out = removeFromInbox(inbox(note('a'), note('b')), 'a')!
    expect(out.pages[0]!.items.map((n) => n.id)).toEqual(['b'])
    expect(out.pages[0]!.total).toBe(1)
    const same = inbox(note('a'))
    expect(removeFromInbox(same, 'zzz')).toBe(same)
  })

  it('replaces an edited note in place', () => {
    const edited = { ...note('n2'), version: 5 }
    expect(replaceOnBoard(board(), edited).categories[0]!.notes[1]!.version).toBe(5)
  })

  it('names the neighbours the server needs to place a note', () => {
    expect(neighbours(['a', 'b', 'c'], 1)).toEqual({ afterId: 'a', beforeId: 'c' })
    expect(neighbours(['a', 'b', 'c'], 0)).toEqual({ afterId: null, beforeId: 'b' })
    expect(neighbours(['a', 'b', 'c'], 2)).toEqual({ afterId: 'b', beforeId: null })
    expect(neighbours(['a'], 0)).toEqual({ afterId: null, beforeId: null })
  })
})
