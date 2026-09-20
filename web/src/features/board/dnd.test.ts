import { describe, expect, it } from 'vitest'
import { announceMoved, findLane, insertIndex, isUnchanged, moveAcross, placementOf, reorder } from './dnd'

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

  it('announces positions in words a screen reader can use', () => {
    expect(announceMoved('over', 'This week', 2, 4)).toBe('Moved to This week, position 3 of 4.')
    expect(announceMoved('drop', 'Done', 0, 1)).toBe('Dropped in Done, position 1 of 1.')
  })
})
