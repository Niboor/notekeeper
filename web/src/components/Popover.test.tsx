import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { Menu } from './Menu'
import { placePopover } from './Popover'

/** A panel of a given size and an anchor at a given place, in a window of 1000 x 800 (jsdom has no layout). */
function setup(anchor: { left: number; top: number; width?: number; height?: number }, panel: { w: number; h: number }) {
  const a = document.createElement('button')
  const r = { left: anchor.left, top: anchor.top, width: anchor.width ?? 30, height: anchor.height ?? 30 }
  a.getBoundingClientRect = () => ({ ...r, right: r.left + r.width, bottom: r.top + r.height, x: r.left, y: r.top, toJSON: () => ({}) })
  const el = document.createElement('div')
  Object.defineProperty(el, 'offsetWidth', { get: () => panel.w })
  Object.defineProperty(el, 'offsetHeight', { get: () => Math.min(panel.h, parseInt(el.style.maxHeight || '99999', 10)) })
  vi.stubGlobal('innerHeight', 800)
  Object.defineProperty(document.documentElement, 'clientWidth', { configurable: true, get: () => 1000 })
  return { a, el }
}
const px = (v: string) => parseInt(v, 10)

afterEach(() => vi.unstubAllGlobals())

// Menus stay inside the window and work although they are rendered outside their button (WEB-N4, WEB-N1).
describe('placePopover', () => {
  it('opens below the button, right-aligned to it', () => {
    const { a, el } = setup({ left: 500, top: 100 }, { w: 190, h: 200 })
    placePopover(el, a, 'right', false)
    expect(px(el.style.top)).toBe(134)
    expect(px(el.style.left)).toBe(340) // its right edge is the button's right edge
  })

  it('shifts sideways instead of running off the left edge (a menu in the first column)', () => {
    const { a, el } = setup({ left: 120, top: 100 }, { w: 190, h: 100 })
    placePopover(el, a, 'right', false)
    expect(px(el.style.left)).toBe(8)
  })

  it('shifts sideways instead of running off the right edge', () => {
    const { a, el } = setup({ left: 980, top: 100, width: 20 }, { w: 190, h: 100 })
    placePopover(el, a, 'left', false)
    expect(px(el.style.left) + 190).toBeLessThanOrEqual(1000 - 8)
  })

  it('flips above the button when there is more room there', () => {
    const { a, el } = setup({ left: 500, top: 700 }, { w: 190, h: 300 })
    placePopover(el, a, 'right', false)
    expect(px(el.style.top) + 300).toBeLessThanOrEqual(700)
    expect(px(el.style.top)).toBeGreaterThanOrEqual(8)
  })

  it('is as tall as the room allows when it fits neither way, and stays on screen', () => {
    const { a, el } = setup({ left: 500, top: 400 }, { w: 190, h: 2000 })
    placePopover(el, a, 'right', false)
    const top = px(el.style.top)
    expect(top).toBeGreaterThanOrEqual(0)
    expect(top + px(el.style.maxHeight)).toBeLessThanOrEqual(800)
  })

  it('leaves the position to the stylesheet for a bottom sheet on a phone', () => {
    const { a, el } = setup({ left: 100, top: 100 }, { w: 190, h: 100 })
    vi.stubGlobal('matchMedia', () => ({ matches: true }))
    placePopover(el, a, 'right', true)
    expect(el.dataset.sheet).toBeDefined()
    expect(el.style.top).toBe('')
  })
})

describe('Menu', () => {
  const items = (onA: () => void) => [
    { label: 'First', onSelect: onA },
    { label: 'Second', onSelect: () => {} },
  ]

  it('runs an item that is clicked, although the list is rendered outside the button', async () => {
    const onA = vi.fn()
    render(<Menu label="More" items={items(onA)}>x</Menu>)
    await userEvent.click(screen.getByRole('button', { name: 'More' }))
    await userEvent.click(screen.getByRole('menuitem', { name: 'First' }))
    expect(onA).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('menu')).toBeNull()
  })

  it('closes on a press outside and on Escape, and gives the focus back to the button', async () => {
    render(
      <div>
        <Menu label="More" items={items(() => {})}>x</Menu>
        <p>elsewhere</p>
      </div>,
    )
    const button = screen.getByRole('button', { name: 'More' })
    await userEvent.click(button)
    expect(screen.getByRole('menu')).toBeTruthy()
    await userEvent.click(screen.getByText('elsewhere'))
    expect(screen.queryByRole('menu')).toBeNull()
    await userEvent.click(button)
    await userEvent.keyboard('{Escape}')
    expect(screen.queryByRole('menu')).toBeNull()
    expect(document.activeElement).toBe(button)
  })

  it('focuses the first item, moves with the arrow keys and closes on Tab', async () => {
    render(<Menu label="More" items={items(() => {})}>x</Menu>)
    await userEvent.click(screen.getByRole('button', { name: 'More' }))
    expect(document.activeElement).toBe(screen.getByRole('menuitem', { name: 'First' }))
    await userEvent.keyboard('{ArrowDown}')
    expect(document.activeElement).toBe(screen.getByRole('menuitem', { name: 'Second' }))
    await userEvent.keyboard('{ArrowDown}')
    expect(document.activeElement).toBe(screen.getByRole('menuitem', { name: 'First' }))
    await userEvent.keyboard('{End}')
    expect(document.activeElement).toBe(screen.getByRole('menuitem', { name: 'Second' }))
    await userEvent.keyboard('{Tab}')
    expect(screen.queryByRole('menu')).toBeNull()
  })
})
