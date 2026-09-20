import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'

interface ToastAction {
  label: string
  onClick: () => void
}

interface ToastOptions {
  message: string
  action?: ToastAction
  /** Milliseconds before it disappears; 10 s when there is an action (WEB-6), 4 s otherwise. */
  timeout?: number
}

interface ToastApi {
  toast: (o: ToastOptions) => void
}

const ToastContext = createContext<ToastApi | null>(null)

export function useToast(): ToastApi {
  const ctx = useContext(ToastContext)
  if (!ctx) throw new Error('useToast outside ToastProvider')
  return ctx
}

/** One transient message at a time, with an optional action such as Undo. */
export function ToastProvider({ children }: { children: ReactNode }) {
  const [current, setCurrent] = useState<(ToastOptions & { id: number }) | null>(null)
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const seq = useRef(0)

  const toast = useCallback((o: ToastOptions) => {
    clearTimeout(timer.current)
    const id = ++seq.current
    setCurrent({ ...o, id })
    timer.current = setTimeout(() => setCurrent((c) => (c?.id === id ? null : c)), o.timeout ?? (o.action ? 10_000 : 4_000))
  }, [])

  useEffect(() => () => clearTimeout(timer.current), [])
  const api = useMemo(() => ({ toast }), [toast])

  return (
    <ToastContext value={api}>
      {children}
      <div className={`toast${current ? ' is-visible' : ''}`} role="status" aria-live="polite">
        {current && (
          <>
            <span>{current.message}</span>
            {current.action && (
              <button
                className="undo"
                onClick={() => {
                  current.action?.onClick()
                  clearTimeout(timer.current)
                  setCurrent(null)
                }}
              >
                {current.action.label}
              </button>
            )}
          </>
        )}
      </div>
    </ToastContext>
  )
}
