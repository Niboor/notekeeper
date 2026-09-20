import { createContext } from 'react'

/** The element in the top bar where the board renders its page tabs (provided by the Layout). */
export const TopbarSlot = createContext<HTMLElement | null>(null)
