// The one component that renders note text, in the application and on the share page
// (SEC-CNT-1, SEC-CNT-2, docs/design/07 section 5). It renders a Markdown syntax tree through an
// allowlist of node types into React elements: no HTML strings, no dangerouslySetInnerHTML (the
// lint rule forbids it), links restricted to safe schemes, raw HTML shown as text.
import type { PhrasingContent, RootContent } from 'mdast'
import { Fragment, useMemo, useState, type ReactNode } from 'react'
import { parseNote, safeHref, toggleTask } from './markdown'

interface Props {
  text: string
  /** Called with the new text when a task-list checkbox is toggled. Omit for a read-only view. */
  onChange?: (newText: string) => void
}

type Node = RootContent | PhrasingContent

function lines(text: string, key: string): ReactNode {
  return text.split('\n').map((p, i) => (
    <Fragment key={`${key}.${i}`}>
      {i > 0 && <br />}
      {p}
    </Fragment>
  ))
}

/** The visible words of a subtree, for labelling checkboxes. */
function plainText(nodes: readonly Node[]): string {
  let out = ''
  for (const n of nodes) {
    if ('value' in n && typeof n.value === 'string') out += n.value
    else if ('children' in n) out += plainText(n.children as Node[])
  }
  return out.trim().slice(0, 120)
}

export function NoteContent({ text, onChange }: Props) {
  const tree = useMemo(() => parseNote(text), [text])
  // A ticked box must show at once, in the same frame as the click. The text comes back through the
  // query cache, which notifies React a tick later, and a controlled checkbox would flash back to
  // its old state in between. So the toggle is remembered locally until the text itself changes.
  const [local, setLocal] = useState<{ base: string; checked: Record<number, boolean> }>({ base: text, checked: {} })
  const overrides = local.base === text ? local.checked : {}

  const render = (node: Node, key: string): ReactNode => {
    const kids = (children: readonly Node[] | undefined) => (children ?? []).map((c, i) => render(c, `${key}.${i}`))
    switch (node.type) {
      case 'text':
        return lines(node.value, key)
      case 'paragraph':
        return <p key={key}>{kids(node.children)}</p>
      case 'heading':
        return (
          <p key={key} className="md-heading">
            <strong>{kids(node.children)}</strong>
          </p>
        )
      case 'strong':
        return <strong key={key}>{kids(node.children)}</strong>
      case 'emphasis':
        return <em key={key}>{kids(node.children)}</em>
      case 'delete':
        return <del key={key}>{kids(node.children)}</del>
      case 'inlineCode':
        return <code key={key}>{node.value}</code>
      case 'code':
        return (
          <pre key={key}>
            <code>{node.value}</code>
          </pre>
        )
      case 'break':
        return <br key={key} />
      case 'thematicBreak':
        return <hr key={key} />
      case 'blockquote':
        return <blockquote key={key}>{kids(node.children)}</blockquote>
      case 'list': {
        const Tag = node.ordered ? 'ol' : 'ul'
        const tasks = node.children.some((c) => typeof c.checked === 'boolean')
        const start = node.ordered && node.start != null && node.start !== 1 ? node.start : undefined
        return (
          <Tag key={key} className={tasks ? 'checklist' : undefined} start={start}>
            {kids(node.children)}
          </Tag>
        )
      }
      case 'listItem': {
        if (typeof node.checked === 'boolean') {
          const offset = node.position?.start.offset
          const checked = offset !== undefined && offset in overrides ? overrides[offset]! : node.checked
          return (
            <li key={key} className={checked ? 'done' : undefined}>
              {/* The words are part of the label: clicking them ticks the box, clicking a link in them opens the link. */}
              <label>
                <input
                  type="checkbox"
                  checked={checked}
                  disabled={!onChange || offset === undefined}
                  aria-label={plainText(node.children) || 'task'}
                  onChange={() => {
                    if (!onChange || offset === undefined) return
                    setLocal({ base: text, checked: { ...overrides, [offset]: !checked } })
                    onChange(toggleTask(text, offset))
                  }}
                />
                <span>{kids(node.children)}</span>
              </label>
            </li>
          )
        }
        return <li key={key}>{kids(node.children)}</li>
      }
      case 'link': {
        const href = safeHref(node.url)
        if (!href) return <Fragment key={key}>{kids(node.children)}</Fragment> // unsafe scheme: keep the words, drop the link
        return (
          <a key={key} href={href} target="_blank" rel="noopener noreferrer nofollow">
            {kids(node.children)}
          </a>
        )
      }
      case 'image': // never fetch remote images on a note's behalf; show what it said instead
        return <Fragment key={key}>{node.alt ?? node.url}</Fragment>
      case 'html':
        return <Fragment key={key}>{lines(node.value, key)}</Fragment> // raw HTML is text
      case 'table':
        return (
          <table key={key}>
            <tbody>{kids(node.children)}</tbody>
          </table>
        )
      case 'tableRow':
        return <tr key={key}>{kids(node.children)}</tr>
      case 'tableCell':
        return <td key={key}>{kids(node.children)}</td>
      default:
        return null // definitions, footnotes and anything unknown are not rendered
    }
  }

  return <div className="text">{tree.children.map((c, i) => render(c, String(i)))}</div>
}
