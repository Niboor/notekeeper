import { useEffect, useMemo, useRef, useState, type HTMLAttributes, type Ref } from 'react'
import { Icon } from '../../components/Icon'
import { Menu, type MenuItem } from '../../components/Menu'
import { NoteContent } from '../../components/NoteContent'
import { taskProgress } from '../../components/markdown'
import { timeAgo } from '../../components/time'
import { t } from '../../i18n'
import { HistoryDialog } from './HistoryDialog'
import { MergeDialog } from './MergeDialog'
import { ReminderDialog } from '../reminders/ReminderDialog'
import { repeatWord } from '../reminders/hooks'
import { ShareDialog, formatWhen } from '../share/ShareDialog'
import { useSharedNoteIds } from '../share/hooks'
import { useAddPart, useSplitPart, useDismissNote, useEditPart, useMoveNote, usePages, useRemovePart } from '../hooks'
import { INBOX, type Note, type NotePart } from '../types'
import { AttachmentView } from './AttachmentView'

function sourceLabel(note: Note): string | null {
  const chat = note.parts.find((p) => p.source_bot_type)
  if (!chat?.source_bot_type) return null
  return chat.source_bot_type.charAt(0).toUpperCase() + chat.source_bot_type.slice(1)
}

interface Props {
  note: Note
  /** dnd-kit wiring: spread onto the card so the whole card is the drag handle. */
  dragProps?: HTMLAttributes<HTMLElement>
  innerRef?: Ref<HTMLElement>
  /** True for the copy shown under the pointer while dragging. */
  overlay?: boolean
  dragging?: boolean
  /** Picked up with the keyboard. */
  lifted?: boolean
  /** Where the note currently is (for "move to" menus); hides the current lane from the list. */
  laneId?: string
  /** The other notes of the same column, to merge from. */
  siblings?: Note[]
}

/** One note as a card: content, attachments, meta line, and its actions. */
export function NoteCard({ note, dragProps, innerRef, overlay, dragging, lifted, laneId, siblings }: Props) {
  const dismiss = useDismissNote()
  const edit = useEditPart()
  const addPart = useAddPart()
  const removePart = useRemovePart()
  const move = useMoveNote()
  const pages = usePages()

  const [editing, setEditing] = useState<{ partId: string | null; base: number } | null>(null)
  const [draft, setDraft] = useState('')
  const [sharing, setSharing] = useState(false)
  const [reminding, setReminding] = useState(false)
  const [merging, setMerging] = useState(false)
  const [viewingHistory, setViewingHistory] = useState(false)
  const split = useSplitPart()
  const shared = useSharedNoteIds().has(note.id)

  const source = sourceLabel(note)
  const textParts = note.parts.filter((p) => p.kind === 'text')
  const progress = useMemo(() => {
    let done = 0
    let total = 0
    for (const p of textParts) {
      const tp = taskProgress(p.text ?? '')
      done += tp.done
      total += tp.total
    }
    return { done, total }
  }, [textParts])

  const startEdit = (part: NotePart | null) => {
    setDraft(part?.text ?? '')
    setEditing({ partId: part?.id ?? null, base: note.version })
  }
  const save = () => {
    if (!editing) return
    const text = draft.trimEnd()
    if (text.trim() === '') return
    if (editing.partId) edit.mutate({ note, partId: editing.partId, text })
    else addPart.mutate({ noteId: note.id, text })
    setEditing(null)
  }

  const moveItems: MenuItem[] = useMemo(() => {
    const items: MenuItem[] = [{ label: t('note.moveTo'), heading: true, onSelect: () => {} }]
    if (laneId !== INBOX) {
      items.push({ label: t('note.moveToInbox'), onSelect: () => move.mutate({ note, categoryId: null, index: 0 }) })
    }
    for (const p of pages.data ?? []) {
      for (const c of p.categories ?? []) {
        if (c.id === laneId) continue
        items.push({ label: `${p.name} › ${c.name}`, onSelect: () => move.mutate({ note, categoryId: c.id, index: 0 }) })
      }
    }
    return items
  }, [pages.data, laneId, move, note])

  const menu: MenuItem[] = [
    ...(textParts[0] ? [{ label: t('note.edit'), onSelect: () => startEdit(textParts[0]!) }] : [{ label: t('note.addText'), onSelect: () => startEdit(null) }]),
    ...(textParts.length > 0 ? [{ label: t('history.action'), onSelect: () => setViewingHistory(true) }] : []),
    { label: t('remind.action'), onSelect: () => setReminding(true) },
    ...(siblings && note.state === 'active' ? [{ label: t('merge.action'), onSelect: () => setMerging(true) }] : []),
    ...(note.state === 'active' && note.parts.length > 1
      ? note.parts.map((p) => ({
          label: `${t('merge.split')}: ${(p.text ?? p.attachment?.filename ?? p.failed?.filename ?? '…').replace(/\s+/g, ' ').slice(0, 30)}`,
          onSelect: () => split.mutate({ noteId: note.id, partId: p.id }),
        }))
      : []),
    { label: t('share.action'), onSelect: () => setSharing(true) },
    ...moveItems,
  ]

  const editorRef = useRef<HTMLTextAreaElement>(null)
  useEffect(() => {
    if (editing) editorRef.current?.focus()
  }, [editing])
  const changedElsewhere = editing !== null && note.version !== editing.base

  return (
    <article
      ref={innerRef}
      className={`note${dragging ? ' is-dragging-source' : ''}${overlay || lifted ? ' is-lifted' : ''}`}
      data-id={note.id}
      tabIndex={overlay ? -1 : 0}
      {...dragProps}
    >
      <div className="note-body">
        {note.parts.map((part) => {
          if (editing?.partId === part.id) return null
          switch (part.kind) {
            case 'text':
              return (
                <NoteContent
                  key={part.id}
                  text={part.text ?? ''}
                  onChange={(next) => edit.mutate({ note, partId: part.id, text: next })}
                />
              )
            case 'unsupported':
              return (
                <p key={part.id} className="text muted">
                  {part.text ?? t('note.unsupported')}
                </p>
              )
            case 'attachment':
              return (
                <div key={part.id} className="attach-row">
                  <AttachmentView part={part} />
                  {note.parts.length > 1 && !overlay && (
                    <button className="icon-btn attach-remove" aria-label={t('common.cancel')} onClick={() => removePart.mutate({ noteId: note.id, partId: part.id })}>
                      <Icon name="x" />
                    </button>
                  )}
                </div>
              )
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
        {editing && (
          <div className="editor">
            {changedElsewhere && (
              <p className="form-error" role="status">
                {t('note.editedElsewhere')}
              </p>
            )}
            <textarea
              ref={editorRef}
              value={draft}
              aria-label={t('note.edit')}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Escape') setEditing(null)
                if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
                  e.preventDefault()
                  save()
                }
                e.stopPropagation() // typing must never start a drag or trigger board shortcuts
              }}
            />
            <div className="composer-bar">
              <span className="grow" />
              <button className="btn" onClick={() => setEditing(null)}>
                {t('note.cancel')}
              </button>
              <button className="btn primary" onClick={save}>
                {t('note.save')}
              </button>
            </div>
          </div>
        )}
      </div>
      <div className="note-meta">
        <span className="origin">
          <Icon name={source ? 'chat' : 'pen'} />
          {source ? t('note.origin.chat', { source }) : t('note.origin.app')} · {timeAgo(note.created_at)}
          {note.parts.some((p) => p.text_edited_at) && ` · ${t('note.edited')}`}
        </span>
        {note.reminders?.filter((r) => r.state === 'pending' || r.state === 'fired').slice(0, 1).map((r) => (
          <button
            key={r.id}
            className={`chip reminder-chip${r.state === 'fired' ? ' fired' : ''}`}
            title={t('remind.title')}
            onClick={() => setReminding(true)}
          >
            <Icon name="bell" />
            {r.state === 'fired' ? t('remind.fired', { when: formatWhen(r.last_fired_at ?? r.due_at) }) : formatWhen(r.due_at)}
            {repeatWord(r.rrule) ? ' ↻' : ''}
          </button>
        ))}
        {shared && (
          <span className="chip" title={t('share.indicator')}>
            <Icon name="link" />
            {t('share.indicator')}
          </span>
        )}
        {progress.total > 0 && (
          <span className="chip" title="Checklist progress">
            <Icon name="check" />
            {t('note.checklistProgress', { done: progress.done, total: progress.total })}
          </span>
        )}
      </div>
      {!overlay && (
        <div className="note-actions">
          <button className="icon-btn dismiss" aria-label={t('note.dismiss')} title={t('note.dismiss')} onClick={() => dismiss.mutate(note)}>
            <Icon name="x" />
          </button>
          <Menu label={t('note.moreActions')} items={menu}>
            <Icon name="more" />
          </Menu>
        </div>
      )}
      {viewingHistory && <HistoryDialog note={note} onClose={() => setViewingHistory(false)} />}
      {merging && <MergeDialog note={note} candidates={(siblings ?? []).filter((n) => n.id !== note.id)} onClose={() => setMerging(false)} />}
      {reminding && <ReminderDialog note={note} onClose={() => setReminding(false)} />}
      {sharing && <ShareDialog noteId={note.id} onClose={() => setSharing(false)} />}
    </article>
  )
}
