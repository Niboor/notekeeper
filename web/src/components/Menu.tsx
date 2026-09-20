import { useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { Popover, useDismiss } from './Popover'

export interface MenuItem {
  label: string
  onSelect: () => void
  danger?: boolean
  /** A heading row (not selectable), used to group the Move-to targets. */
  heading?: boolean
}

interface Props {
  /** Accessible name of the button that opens the menu. */
  label: string
  items: MenuItem[]
  children: ReactNode
  className?: string
  align?: 'left' | 'right'
}

/**
 * A small popup menu: opens on click, closes on outside click or Escape, moves between items
 * with the arrow keys and selects with Enter or Space (WEB-N4). The list floats above the page
 * (see Popover): it is never cut off by a scrolling column or the tab strip, stays inside the
 * window, and is a bottom sheet on phones.
 */
export function Menu({ label, items, children, className = '', align = 'right' }: Props) {
  const [open, setOpen] = useState(false)
  const button = useRef<HTMLButtonElement>(null)
  const list = useRef<HTMLDivElement>(null)
  const menuId = useId()

  useDismiss(open, () => {
    setOpen(false)
    button.current?.focus()
  }, [button, list])

  useEffect(() => {
    if (open) list.current?.querySelector<HTMLElement>('[role=menuitem]')?.focus()
  }, [open])

  const menuItems = () => Array.from(list.current?.querySelectorAll<HTMLElement>('[role=menuitem]') ?? [])
  const move = (to: 'next' | 'prev' | 'first' | 'last') => {
    const els = menuItems()
    const i = els.indexOf(document.activeElement as HTMLElement)
    const target = to === 'first' ? 0 : to === 'last' ? els.length - 1 : (i + (to === 'next' ? 1 : -1) + els.length) % els.length
    els[target]?.focus()
  }

  return (
    <div className={`menu-anchor ${className}`}>
      <button
        ref={button}
        type="button"
        className="icon-btn"
        aria-label={label}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        onClick={() => setOpen((v) => !v)}
      >
        {children}
      </button>
      {open && (
        <Popover
          id={menuId}
          anchor={button}
          popRef={list}
          align={align}
          sheet
          role="menu"
          className="pop-menu"
          onKeyDown={(e) => {
            const keys = { ArrowDown: 'next', ArrowUp: 'prev', Home: 'first', End: 'last' } as const
            if (e.key in keys) {
              e.preventDefault()
              move(keys[e.key as keyof typeof keys])
            }
            if (e.key === 'Tab') {
              // Leaving the list closes it; the focus continues from the button.
              setOpen(false)
              button.current?.focus()
            }
          }}
        >
          {items.map((it, i) =>
            it.heading ? (
              <div key={i} className="pop-heading" role="presentation">
                {it.label}
              </div>
            ) : (
              <button
                key={i}
                role="menuitem"
                type="button"
                className={it.danger ? 'danger' : undefined}
                onClick={() => {
                  setOpen(false)
                  it.onSelect()
                }}
              >
                {it.label}
              </button>
            ),
          )}
        </Popover>
      )}
    </div>
  )
}
