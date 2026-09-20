import { useEffect, useEffectEvent, useLayoutEffect, type HTMLAttributes, type RefObject } from 'react'
import { createPortal } from 'react-dom'

const GAP = 4
const MARGIN = 8
/** Below this width a menu is a bottom sheet instead of a popup next to its button. */
const SHEET_QUERY = '(max-width: 820px)'

/**
 * Puts a floating panel next to its anchor and keeps it inside the window: below the anchor when
 * there is room, above it when there is more room there, shifted sideways so no edge is cut off,
 * and as tall as the room allows (the panel scrolls). With `sheet` it is a bottom sheet on phones,
 * which the stylesheet positions.
 */
export function placePopover(el: HTMLElement, anchor: HTMLElement, align: 'left' | 'right', sheet: boolean) {
  if (sheet && typeof window.matchMedia === 'function' && window.matchMedia(SHEET_QUERY).matches) {
    el.dataset.sheet = ''
    el.style.left = el.style.top = el.style.maxHeight = ''
    return
  }
  delete el.dataset.sheet
  const scrolled = el.scrollTop // measuring below removes the height limit for a moment, which would reset it
  const a = anchor.getBoundingClientRect()
  const vw = document.documentElement.clientWidth
  const vh = window.innerHeight
  el.style.maxHeight = ''
  el.style.left = '0px'
  el.style.top = '0px'
  const w = el.offsetWidth
  const natural = el.offsetHeight
  const below = vh - a.bottom - GAP - MARGIN
  const above = a.top - GAP - MARGIN
  const flip = natural > below && above > below
  const room = Math.max(flip ? above : below, 96)
  const height = Math.min(natural, room)
  const left = align === 'right' ? a.right - w : a.left
  el.style.maxHeight = `${Math.floor(room)}px`
  el.style.left = `${Math.round(Math.min(Math.max(left, MARGIN), Math.max(vw - w - MARGIN, MARGIN)))}px`
  el.style.top = `${Math.round(flip ? a.top - GAP - height : a.bottom + GAP)}px`
  el.scrollTop = scrolled
}

interface PopoverProps extends HTMLAttributes<HTMLDivElement> {
  /** The element the panel belongs to. */
  anchor: RefObject<HTMLElement | null>
  /** The panel itself, for the caller's focus handling and "click outside" checks. */
  popRef: RefObject<HTMLDivElement | null>
  align?: 'left' | 'right'
  sheet?: boolean
}

/**
 * A floating panel rendered at the end of <body>, so that no scrolling or clipping ancestor (the
 * page tabs, a column, a sticky lane) can cut it off or paint over it.
 */
export function Popover({ anchor, popRef, align = 'right', sheet = false, className = '', ...rest }: PopoverProps) {
  useLayoutEffect(() => {
    const el = popRef.current
    const a = anchor.current
    if (!el || !a) return
    const place = () => placePopover(el, a, align, sheet)
    // Scrolling anything the anchor sits in moves the panel with it; scrolling the panel itself does not.
    const follow = (e: Event) => {
      if (!el.contains(e.target as Node)) place()
    }
    place()
    window.addEventListener('resize', place)
    window.addEventListener('scroll', follow, true)
    return () => {
      window.removeEventListener('resize', place)
      window.removeEventListener('scroll', follow, true)
    }
  }, [anchor, popRef, align, sheet])
  return createPortal(<div {...rest} ref={popRef} className={`popover ${className}`} />, document.body)
}

/** Closes an open panel on Escape or a press outside every one of `inside` (the button and the panel). */
export function useDismiss(open: boolean, close: () => void, inside: RefObject<HTMLElement | null>[]) {
  const onPress = useEffectEvent((e: MouseEvent) => {
    if (!inside.some((r) => r.current?.contains(e.target as Node))) close()
  })
  const onKey = useEffectEvent((e: KeyboardEvent) => {
    if (e.key === 'Escape') close()
  })
  useEffect(() => {
    if (!open) return
    document.addEventListener('mousedown', onPress)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onPress)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])
}
