import { useCallback, useState } from 'react'

export type Theme = 'light' | 'dark'

const KEY = 'nk-theme'

function stored(): Theme | null {
  try {
    const v = localStorage.getItem(KEY)
    return v === 'light' || v === 'dark' ? v : null
  } catch {
    return null // storage can be blocked; the system preference still works
  }
}

function system(): Theme {
  return window.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

/** Applies a stored choice before first paint. The system preference applies until the user chooses. */
export function applyStoredTheme(): void {
  const t = stored()
  if (t) document.documentElement.dataset.theme = t
}

/** The current theme and a toggle that remembers the choice. */
export function useTheme(): [Theme, () => void] {
  const [theme, setTheme] = useState<Theme>(() => stored() ?? system())
  const toggle = useCallback(() => {
    const next: Theme = theme === 'dark' ? 'light' : 'dark'
    document.documentElement.dataset.theme = next
    try {
      localStorage.setItem(KEY, next)
    } catch {
      /* not persisted; still applied for this visit */
    }
    setTheme(next)
  }, [theme])
  return [theme, toggle]
}
