import { render } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { safeHref, taskProgress, toggleTask } from './markdown'
import { NoteContent } from './NoteContent'

// Hostile inputs of every kind. Whatever they contain, the rendered DOM must hold no script,
// frame, embedded object, remote image or event-handler attribute, and every link must be safe
// (SEC-CNT-1, SEC-CNT-2, WEB-N5).
const CORPUS = [
  '<script>alert(1)</script>',
  '<img src=x onerror=alert(1)>',
  '<svg onload=alert(1)><circle/></svg>',
  '<iframe src="javascript:alert(1)"></iframe>',
  '<a href="javascript:alert(1)">click</a>',
  '[click](javascript:alert(1))',
  '[click](JaVaScRiPt:alert(1))',
  '[click](java\tscript:alert(1))',
  '[click]( javascript:alert(1))',
  '[click](data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==)',
  '[click](vbscript:msgbox(1))',
  '[click](//evil.example/x)',
  '[click](/relative/path)',
  '![img](https://evil.example/track.png)',
  '![x](javascript:alert(1))',
  '<div style="background:url(javascript:alert(1))">x</div>',
  '<object data="x"></object><embed src="x"><form action="x"><input name=q></form>',
  '<style>*{display:none}</style>',
  '<meta http-equiv="refresh" content="0;url=https://evil.example">',
  '<base href="https://evil.example/">',
  '&lt;script&gt;alert(1)&lt;/script&gt;',
  '`<script>alert(1)</script>`',
  '```html\n<script>alert(1)</script>\n```',
  '> <img src=x onerror=alert(1)>',
  '- <img src=x onerror=alert(1)>\n- [ ] <script>x</script>',
  '**<b onmouseover=alert(1)>bold</b>**',
  'https://example.com/<script>',
  'javascript:alert(1)',
  '<https://evil.example/" onclick="alert(1)>',
  '[a](https://example.com "title\\" onclick=\\"alert(1)")',
  '<!-- comment --><script>alert(1)</script>',
  '<details open ontoggle=alert(1)>x</details>',
  '<math><mi xlink:href="javascript:alert(1)">x</mi></math>',
  String.fromCharCode(0x202e) + '<script>alert(1)</script>',
]

describe('NoteContent XSS corpus', () => {
  it.each(CORPUS)('renders %j inertly', (text) => {
    const { container, unmount } = render(<NoteContent text={text} />)
    for (const tag of ['script', 'iframe', 'object', 'embed', 'form', 'svg', 'math', 'img', 'style', 'meta', 'base', 'input[type=text]']) {
      expect(container.querySelector(tag), `unexpected <${tag}>`).toBeNull()
    }
    for (const el of container.querySelectorAll('*')) {
      for (const attr of Array.from(el.attributes)) {
        expect(attr.name.startsWith('on'), `event handler ${attr.name}`).toBe(false)
        expect(attr.name, 'inline style').not.toBe('style')
      }
    }
    for (const a of container.querySelectorAll('a')) {
      const href = a.getAttribute('href') ?? ''
      expect(/^(https?:|mailto:|tel:)/i.test(href), `unsafe href ${href}`).toBe(true)
      expect(a.getAttribute('rel')).toContain('noopener')
    }
    unmount()
  })

  it('shows raw HTML as text instead of dropping it', () => {
    const { container } = render(<NoteContent text={'<b>hello</b>'} />)
    expect(container.textContent).toContain('<b>hello</b>')
    expect(container.querySelector('b')).toBeNull()
  })
})

// Notes render as formatted text with clickable, safe links and task lists (WEB-8).
describe('NoteContent rendering', () => {
  it('renders formatting, links and line breaks', () => {
    const { container } = render(<NoteContent text={'**bold** and *italic*\nsecond line\n\nsee https://example.org/a?b=1 and [docs](https://example.com)'} />)
    expect(container.querySelector('strong')?.textContent).toBe('bold')
    expect(container.querySelector('em')?.textContent).toBe('italic')
    expect(container.querySelectorAll('br').length).toBeGreaterThan(0)
    const hrefs = Array.from(container.querySelectorAll('a')).map((a) => a.getAttribute('href'))
    expect(hrefs).toEqual(['https://example.org/a?b=1', 'https://example.com'])
    expect(container.querySelector('a')?.getAttribute('target')).toBe('_blank')
  })

  it('keeps the words of a link with an unsafe scheme but drops the link', () => {
    const { container } = render(<NoteContent text="[read me](javascript:alert(1))" />)
    expect(container.textContent).toContain('read me')
    expect(container.querySelector('a')).toBeNull()
  })

  it('renders task lists as checkboxes that toggle exactly one box (CORE-N17)', async () => {
    const text = '- [ ] eggs\n- [x] milk\n- [ ] bread'
    const onChange = vi.fn()
    const { container } = render(<NoteContent text={text} onChange={onChange} />)
    const boxes = Array.from(container.querySelectorAll<HTMLInputElement>('input[type=checkbox]'))
    expect(boxes.map((b) => b.checked)).toEqual([false, true, false])
    expect(boxes[0]?.getAttribute('aria-label')).toBe('eggs')
    await userEvent.setup().click(boxes[2]!)
    expect(onChange).toHaveBeenCalledWith('- [ ] eggs\n- [x] milk\n- [x] bread')
  })

  it('is read-only without a change handler (share page)', () => {
    const { container } = render(<NoteContent text={'- [ ] a'} />)
    expect(container.querySelector<HTMLInputElement>('input[type=checkbox]')?.disabled).toBe(true)
  })
})

describe('markdown helpers', () => {
  it('toggles only the targeted checkbox, whatever surrounds it', () => {
    const text = 'intro [x] not a task\n\n1. [ ] first\n   - [ ] nested\n2. [x] second\n\ntext [ ] again'
    const first = text.indexOf('1. [ ]')
    expect(toggleTask(text, first)).toBe(text.replace('1. [ ] first', '1. [x] first'))
    const nested = text.indexOf('- [ ] nested')
    expect(toggleTask(text, nested)).toBe(text.replace('- [ ] nested', '- [x] nested'))
    expect(toggleTask(text, text.indexOf('2. [x]'))).toBe(text.replace('2. [x] second', '2. [ ] second'))
    expect(toggleTask(text, 0)).toBe(text) // no checkbox starts there
    expect(toggleTask('- [X] upper', 0)).toBe('- [ ] upper')
  })

  it('counts task progress', () => {
    expect(taskProgress('- [x] a\n- [ ] b\n- [ ] c')).toEqual({ done: 1, total: 3 })
    expect(taskProgress('- plain\n- list')).toEqual({ done: 0, total: 0 })
  })

  it('accepts only safe link schemes', () => {
    for (const ok of ['https://example.com', 'http://example.com/a?b', 'mailto:me@example.com', 'tel:+3212345']) expect(safeHref(ok), ok).toBe(ok)
    for (const bad of ['javascript:alert(1)', 'JAVASCRIPT:alert(1)', ' javascript:alert(1)', 'java\nscript:alert(1)', 'data:text/html,x', 'vbscript:x', '//evil.example', '/local', 'file:///etc/passwd', '', 'blob:https://x/y'])
      expect(safeHref(bad), JSON.stringify(bad)).toBeNull()
  })
})
