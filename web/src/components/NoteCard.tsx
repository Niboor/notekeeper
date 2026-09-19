import { t } from '../i18n'
import type { Schemas } from '../api/client'
import { Icon } from './Icon'
import { NoteContent } from './NoteContent'
import { timeAgo } from './time'

type Note = Schemas['Note']

function sourceLabel(note: Note): string | null {
  const chat = note.parts.find((p) => p.source_bot_type)
  if (!chat?.source_bot_type) return null
  return chat.source_bot_type.charAt(0).toUpperCase() + chat.source_bot_type.slice(1)
}

/** One note as a card: content, then where and when it came from. */
export function NoteCard({ note }: { note: Note }) {
  const source = sourceLabel(note)
  return (
    <article className="note" data-id={note.id}>
      <div className="note-body">
        {note.parts.map((part) => {
          switch (part.kind) {
            case 'text':
            case 'unsupported':
              return <NoteContent key={part.id} text={part.text ?? t('note.unsupported')} />
            case 'failed_attachment':
              return (
                <div className="file" key={part.id}>
                  <Icon name="file" />
                  <span className="fname">{t('note.failedAttachment', { name: part.failed?.filename ?? '' })}</span>
                </div>
              )
            default:
              return null
          }
        })}
      </div>
      <div className="note-meta">
        <span className="origin">
          <Icon name={source ? 'chat' : 'pen'} />
          {source ? t('note.origin.chat', { source }) : t('note.origin.app')} · {timeAgo(note.created_at)}
        </span>
      </div>
    </article>
  )
}
