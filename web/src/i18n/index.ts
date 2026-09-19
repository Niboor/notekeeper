import { en, type MessageKey } from './en'

/** Looks up a message and fills `{name}` placeholders. */
export function t(key: MessageKey, vars?: Record<string, string | number>): string {
  let text: string = en[key]
  if (vars) {
    for (const [name, value] of Object.entries(vars)) {
      text = text.replaceAll(`{${name}}`, String(value))
    }
  }
  return text
}

export type { MessageKey }
