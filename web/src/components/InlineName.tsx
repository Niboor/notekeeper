import { useEffect, useRef, useState } from 'react'
import { t } from '../i18n'

interface Props {
  initial?: string
  placeholder: string
  onSubmit: (name: string) => void
  onCancel: () => void
  className?: string
}

/** A one-line name field: Enter saves, Escape or clicking away cancels. */
export function InlineName({ initial = '', placeholder, onSubmit, onCancel, className = '' }: Props) {
  const [value, setValue] = useState(initial)
  const ref = useRef<HTMLInputElement>(null)
  useEffect(() => {
    ref.current?.focus()
    ref.current?.select()
  }, [])
  const submit = () => {
    const name = value.trim()
    if (name === '' || name === initial) onCancel()
    else onSubmit(name)
  }
  return (
    <input
      ref={ref}
      className={`inline-name ${className}`}
      value={value}
      placeholder={placeholder}
      aria-label={placeholder || t('common.save')}
      maxLength={100}
      onChange={(e) => setValue(e.target.value)}
      onBlur={onCancel}
      onKeyDown={(e) => {
        e.stopPropagation()
        if (e.key === 'Enter') {
          e.preventDefault()
          submit()
        }
        if (e.key === 'Escape') onCancel()
      }}
    />
  )
}
