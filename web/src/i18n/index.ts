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

/** The keys that have a singular form under the same name plus ".one". */
export type CountKey = { [K in MessageKey]: `${K}.one` extends MessageKey ? K : never }[MessageKey]

/** Like t, for a message about a number of things: "1 view", "2 views". */
export function tn(key: CountKey, count: number): string {
  return t(count === 1 ? (`${key}.one` as MessageKey) : key, { count })
}

export type { MessageKey }
