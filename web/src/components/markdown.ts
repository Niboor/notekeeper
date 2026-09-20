import type { Root } from 'mdast'
import { fromMarkdown } from 'mdast-util-from-markdown'
import { gfmFromMarkdown } from 'mdast-util-gfm'
import { gfm } from 'micromark-extension-gfm'

/**
 * Parses note text as CommonMark plus the GitHub extensions notes need (task lists, bare-URL
 * links, strikethrough). The result is a syntax tree, never HTML: the renderer builds React
 * elements from an allowlist of node types, so nothing a note contains can become markup
 * (SEC-CNT-1, SEC-CNT-2). Raw HTML in the text stays a plain string node.
 */
export function parseNote(text: string): Root {
  return fromMarkdown(text, { extensions: [gfm()], mdastExtensions: [gfmFromMarkdown()] })
}

const ALLOWED_PROTOCOLS = new Set(['http:', 'https:', 'mailto:', 'tel:'])

/** Characters browsers ignore inside a URL scheme (controls, spaces, zero-width and line separators). */
function isIgnorableInScheme(code: number): boolean {
  return (
    code <= 0x20 ||
    (code >= 0x7f && code <= 0x9f) ||
    (code >= 0x200b && code <= 0x200f) ||
    code === 0x2028 ||
    code === 0x2029 ||
    code === 0xfeff
  )
}

/**
 * Returns the URL if it is safe to link to, otherwise null. Only http, https, mailto and tel are
 * linked; `javascript:`, `data:`, `vbscript:` and anything unparseable are refused, including
 * spellings with embedded whitespace or control characters that browsers skip.
 */
export function safeHref(raw: string): string | null {
  let cleaned = ''
  for (const ch of raw) {
    if (!isIgnorableInScheme(ch.codePointAt(0) ?? 0)) cleaned += ch
  }
  if (cleaned === '' || !/^(https?:|mailto:|tel:)/i.test(cleaned)) return null
  try {
    const url = new URL(cleaned)
    return ALLOWED_PROTOCOLS.has(url.protocol) ? raw.trim() : null
  } catch {
    return null
  }
}

const TASK_MARKER = /^(\s*(?:[-*+]|\d+[.)])\s+)\[( |x|X)\]/

/**
 * Toggles the task-list checkbox of the list item that starts at `offset` in `text`, changing
 * exactly that `[ ]` or `[x]` and nothing else (CORE-N17). Returns the text unchanged when no
 * checkbox starts there.
 */
export function toggleTask(text: string, offset: number): string {
  const m = TASK_MARKER.exec(text.slice(offset))
  if (!m) return text
  const at = offset + (m[1]?.length ?? 0) + 1
  return text.slice(0, at) + (m[2] === ' ' ? 'x' : ' ') + text.slice(at + 1)
}

export interface TaskProgress {
  done: number
  total: number
}

interface TreeNode {
  type: string
  checked?: boolean | null
  children?: unknown[]
}

/** Counts checked and total task-list items, for the progress badge on a card. */
export function taskProgress(text: string): TaskProgress {
  let done = 0
  let total = 0
  const walk = (node: TreeNode) => {
    if (node.type === 'listItem' && typeof node.checked === 'boolean') {
      total++
      if (node.checked) done++
    }
    for (const child of node.children ?? []) walk(child as TreeNode)
  }
  walk(parseNote(text))
  return { done, total }
}
