import { useEffect, useId, useRef, useState, type ReactNode } from 'react'

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
 * with the arrow keys and selects with Enter or Space (WEB-N4).
 */
export function Menu({ label, items, children, className = '', align = 'right' }: Props) {
  const [open, setOpen] = useState(false)
  const root = useRef<HTMLDivElement>(null)
  const menuId = useId()

  useEffect(() => {
    if (!open) return
    const outside = (e: MouseEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false)
    }
    const escape = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setOpen(false)
        root.current?.querySelector<HTMLElement>('[aria-haspopup]')?.focus()
      }
    }
    document.addEventListener('mousedown', outside)
    document.addEventListener('keydown', escape)
    root.current?.querySelector<HTMLElement>('[role=menuitem]')?.focus()
    return () => {
      document.removeEventListener('mousedown', outside)
      document.removeEventListener('keydown', escape)
    }
  }, [open])

  const move = (dir: 1 | -1) => {
    const els = Array.from(root.current?.querySelectorAll<HTMLElement>('[role=menuitem]') ?? [])
    const i = els.indexOf(document.activeElement as HTMLElement)
    els[(i + dir + els.length) % els.length]?.focus()
  }

  return (
    <div className={`menu-anchor ${className}`} ref={root}>
      <button
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
        <div
          id={menuId}
          role="menu"
          className={`pop-menu align-${align}`}
          onKeyDown={(e) => {
            if (e.key === 'ArrowDown') {
              e.preventDefault()
              move(1)
            }
            if (e.key === 'ArrowUp') {
              e.preventDefault()
              move(-1)
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
        </div>
      )}
    </div>
  )
}
