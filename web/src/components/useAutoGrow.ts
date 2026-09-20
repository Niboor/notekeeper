import { useLayoutEffect, type RefObject } from 'react'

/** Makes a text area as tall as its text (the stylesheet caps it and lets it scroll beyond that). */
export function useAutoGrow(ref: RefObject<HTMLTextAreaElement | null>, value: string | null) {
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${el.scrollHeight + (el.offsetHeight - el.clientHeight)}px`
  }, [ref, value])
}
