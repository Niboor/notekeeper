import { describe, expect, it } from 'vitest'
import { tn } from '../i18n'
import { plainExcerpt } from './markdown'

describe('plainExcerpt', () => {
  it('drops list, task, heading and quote marks and joins the lines', () => {
    expect(plainExcerpt('- [ ] tickets\n- [x] hotel\n- rental car')).toBe('tickets · hotel · rental car')
    expect(plainExcerpt('# Title\n\n> quoted\n1. one\n2) two')).toBe('Title · quoted · one · two')
    expect(plainExcerpt('**bold** and `code` and ~~gone~~')).toBe('bold and code and gone')
  })

  it('keeps underscores inside words and cuts long text with an ellipsis', () => {
    expect(plainExcerpt('my_file_name.txt')).toBe('my_file_name.txt')
    const cut = plainExcerpt('word '.repeat(50), 20)
    expect(cut.length).toBeLessThanOrEqual(20)
    expect(cut.endsWith('…')).toBe(true)
  })

  it('is empty for text with nothing to show', () => {
    expect(plainExcerpt('\n\n  \n')).toBe('')
  })
})

describe('tn', () => {
  it('uses the singular form for exactly one', () => {
    expect(tn('share.views', 1)).toBe('1 view')
    expect(tn('share.views', 0)).toBe('0 views')
    expect(tn('share.views', 2)).toBe('2 views')
    expect(tn('lane.count', 1)).toBe('1 note')
  })
})
