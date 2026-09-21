import { describe, expect, it } from 'vitest'
import {
  announceMoved, colDragId, colOf, colZoneId, columnDrop, columnPlace, columnUnchanged, findLane, insertIndex, isColDrag, isColZone, isLaneDrop, isUnchanged, keyboardStep, moveAcross,
  placementOf, rebase, reorder, withoutColumn,
} from './dnd'

const arr = () => ({ inbox: ['i1', 'i2'], todo: ['a', 'b', 'c'], done: [] as string[] })

describe('drag arrangement', () => {
  it('moves a note across lanes at an index, also into an empty lane', () => {
    expect(moveAcross(arr(), 'i1', 'todo', 1)).toEqual({ inbox: ['i2'], todo: ['a', 'i1', 'b', 'c'], done: [] })
    expect(moveAcross(arr(), 'b', 'done', 0)).toEqual({ inbox: ['i1', 'i2'], todo: ['a', 'c'], done: ['b'] })
  })

  it('accepts a note that came from another page (it is in no lane yet)', () => {
    expect(moveAcross(arr(), 'from-elsewhere', 'todo', 99).todo).toEqual(['a', 'b', 'c', 'from-elsewhere'])
  })

  it('ignores an unknown target lane', () => {
    const a = arr()
    expect(moveAcross(a, 'a', 'nowhere', 0)).toBe(a)
  })

  it('reorders inside a lane', () => {
    expect(reorder(arr(), 'todo', 'a', 'c').todo).toEqual(['b', 'c', 'a'])
    expect(reorder(arr(), 'todo', 'c', 'a').todo).toEqual(['c', 'a', 'b'])
    const a = arr()
    expect(reorder(a, 'todo', 'a', 'a')).toBe(a)
  })

  it('chooses the insertion index from the pointer half', () => {
    expect(insertIndex(['a', 'b', 'c'], 'b', false)).toBe(1)
    expect(insertIndex(['a', 'b', 'c'], 'b', true)).toBe(2)
    expect(insertIndex(['a', 'b'], 'zzz', false)).toBe(2)
  })

  it('finds where a note is and detects a drop that changes nothing', () => {
    expect(findLane(arr(), 'b')).toBe('todo')
    expect(findLane(arr(), 'zzz')).toBeUndefined()
    const before = placementOf(arr(), 'b')
    expect(isUnchanged(before, placementOf(arr(), 'b'))).toBe(true)
    expect(isUnchanged(before, placementOf(moveAcross(arr(), 'b', 'todo', 0), 'b'))).toBe(false)
    expect(isUnchanged(before, placementOf(moveAcross(arr(), 'b', 'done', 0), 'b'))).toBe(false)
    expect(isUnchanged(null, before)).toBe(false)
  })

  it('moves a lifted note with the arrow keys, including into empty lanes and back to the Inbox', () => {
    const lanes = ['inbox', 'todo', 'done']
    const start = arr()
    expect(keyboardStep(start, lanes, 'b', 'ArrowUp')!.todo).toEqual(['b', 'a', 'c'])
    expect(keyboardStep(start, lanes, 'b', 'ArrowDown')!.todo).toEqual(['a', 'c', 'b'])
    expect(keyboardStep(start, lanes, 'b', 'ArrowRight')).toEqual({ inbox: ['i1', 'i2'], todo: ['a', 'c'], done: ['b'] }) // empty lane
    expect(keyboardStep(start, lanes, 'b', 'ArrowLeft')!.inbox).toEqual(['i1', 'b', 'i2']) // keeps its row, clamped
    expect(keyboardStep(start, lanes, 'a', 'ArrowUp')).toBeNull() // already at the top
    expect(keyboardStep(start, lanes, 'c', 'ArrowDown')).toBeNull()
    expect(keyboardStep(start, lanes, 'i1', 'ArrowLeft')).toBeNull() // nothing left of the Inbox
    expect(keyboardStep(moveAcross(start, 'b', 'done', 0), lanes, 'b', 'ArrowRight')).toBeNull()
    expect(keyboardStep(start, lanes, 'b', 'x')).toBeNull()
    expect(keyboardStep(start, lanes, 'nope', 'ArrowUp')).toBeNull()
  })

  it('rebases a drag when the lanes change under it, e.g. after opening another page', () => {
    const during = moveAcross(arr(), 'a', 'done', 0) // dragging 'a', now sitting in "done" of the first page
    const otherPage = { inbox: ['i1', 'i2'], arrivals: ['x'] } // the second page has different lanes
    const rebased = rebase(during, otherPage, 'a')
    expect(rebased).toEqual({ inbox: ['i1', 'i2'], arrivals: ['x'] }) // 'a' is in no lane until dragged over one
    expect(moveAcross(rebased, 'a', 'arrivals', 1).arrivals).toEqual(['x', 'a'])
    // Unchanged lanes are left alone.
    expect(rebase(during, arr(), 'a')).toBe(during)
    // A lane that still exists keeps the dragged note where it was.
    const sameInbox = rebase({ inbox: ['i1', 'a', 'i2'], gone: [] }, { inbox: ['i1', 'a', 'i2'], other: [] }, 'a')
    expect(sameInbox.inbox).toEqual(['i1', 'a', 'i2'])
  })

  it('announces positions in words a screen reader can use', () => {
    expect(announceMoved('over', 'This week', 2, 4)).toBe('Moved to This week, position 3 of 4.')
    expect(announceMoved('drop', 'Done', 0, 1)).toBe('Dropped in Done, position 1 of 1.')
  })
})

describe('column drops', () => {
  const order = ['a', 'b', 'c', 'd']
  const others = withoutColumn(order, 'b') // a c d

  it('lands before or after the column it is over', () => {
    expect(columnDrop(others, 'c', false)).toEqual({ index: 1, afterId: 'a', beforeId: 'c' })
    expect(columnDrop(others, 'c', true)).toEqual({ index: 2, afterId: 'c', beforeId: 'd' })
    expect(columnDrop(others, 'a', false)).toEqual({ index: 0, afterId: null, beforeId: 'a' })
    expect(columnDrop(others, 'd', true)).toEqual({ index: 3, afterId: 'd', beforeId: null })
  })

  it('goes to the end when what it is over is not among the columns, and clamps places', () => {
    expect(columnDrop(others, 'zzz', false)).toEqual({ index: 3, afterId: 'd', beforeId: null })
    expect(columnPlace(others, 99).index).toBe(3)
    expect(columnPlace([], 0)).toEqual({ index: 0, afterId: null, beforeId: null })
  })

  it('knows a drop that changes nothing', () => {
    expect(columnUnchanged('p', 1, 'p', columnDrop(others, 'c', false))).toBe(true) // back where b was
    expect(columnUnchanged('p', 1, 'p', columnDrop(others, 'c', true))).toBe(false)
    expect(columnUnchanged('p', 1, 'q', columnDrop(others, 'c', false))).toBe(false) // another page is always a move
  })

  it('tells the kinds of ids apart', () => {
    expect(isColDrag(colDragId('x'))).toBe(true)
    expect(isColDrag(colZoneId('x'))).toBe(false)
    expect(isColZone(colZoneId('x'))).toBe(true)
    expect(isLaneDrop(colZoneId('x'))).toBe(false)
    expect(colOf(colDragId('x'))).toBe('x')
    expect(colOf(colZoneId('x'))).toBe('x')
  })
})
