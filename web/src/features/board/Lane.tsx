import { useDroppable } from '@dnd-kit/core'
import { SortableContext, useSortable, verticalListSortingStrategy } from '@dnd-kit/sortable'
import { CSS } from '@dnd-kit/utilities'
import { useState } from 'react'
import { Icon } from '../../components/Icon'
import { InlineName } from '../../components/InlineName'
import { Menu } from '../../components/Menu'
import { t } from '../../i18n'
import { Composer } from '../notes/Composer'
import { NoteCard } from '../notes/NoteCard'
import { INBOX, type Note } from '../types'
import { laneDropId } from './dnd'

function SortableNote({ note, laneId }: { note: Note; laneId: string }) {
  const { attributes, listeners, setNodeRef, setActivatorNodeRef, transform, transition, isDragging } = useSortable({ id: note.id })
  return (
    <NoteCard
      note={note}
      laneId={laneId}
      dragging={isDragging}
      innerRef={(el) => {
        setNodeRef(el)
        setActivatorNodeRef(el)
      }}
      dragProps={{ ...attributes, ...listeners, style: { transform: CSS.Translate.toString(transform), transition } }}
    />
  )
}

interface Props {
  id: string
  name: string
  notes: Note[]
  total: number
  current: boolean // the visible lane on a phone
  composerOpen: boolean
  onOpenComposer: () => void
  onCloseComposer: () => void
  hasMore?: boolean
  onShowMore?: () => void
  onRename?: (name: string) => void
  onDelete?: () => void
}

/** One column: the Inbox or a category. Notes are draggable; an empty lane still accepts drops. */
export function Lane({ id, name, notes, total, current, composerOpen, onOpenComposer, onCloseComposer, hasMore, onShowMore, onRename, onDelete }: Props) {
  const { setNodeRef, isOver } = useDroppable({ id: laneDropId(id) })
  const [renaming, setRenaming] = useState(false)
  const isInbox = id === INBOX

  return (
    <section
      ref={setNodeRef}
      className={`lane${isInbox ? ' inbox' : ''}${current ? ' is-current' : ''}${isOver ? ' is-drop-target' : ''}`}
      data-lane={id}
      aria-label={name}
    >
      <div className="lane-head">
        {renaming ? (
          <InlineName initial={name} placeholder={t('lane.columnPlaceholder')} onCancel={() => setRenaming(false)} onSubmit={(n) => (onRename?.(n), setRenaming(false))} />
        ) : (
          <h2>
            {isInbox && <Icon name="inbox" />}
            {name}
          </h2>
        )}
        <span className="count" aria-label={t('lane.count', { count: total })}>
          {total}
        </span>
        {!isInbox && !renaming && (
          <Menu
            label={`${name}: ${t('nav.moreForPage')}`}
            className="lane-menu"
            items={[
              { label: t('lane.rename'), onSelect: () => setRenaming(true) },
              { label: t('lane.delete'), danger: true, onSelect: () => onDelete?.() },
            ]}
          >
            <Icon name="more" />
          </Menu>
        )}
      </div>
      <SortableContext items={notes.map((n) => n.id)} strategy={verticalListSortingStrategy}>
        <div className="lane-notes">
          {composerOpen && <Composer categoryId={isInbox ? null : id} onClose={onCloseComposer} />}
          {notes.map((n) => (
            <SortableNote key={n.id} note={n} laneId={id} />
          ))}
          {notes.length === 0 && !composerOpen && <p className="empty">{isInbox ? t('inbox.empty') : t('lane.empty')}</p>}
          {hasMore && (
            <button className="btn load-more" onClick={onShowMore}>
              {isInbox ? t('inbox.loadMore') : t('lane.showMore')}
            </button>
          )}
          {!composerOpen && (
            <button className="add-row" onClick={onOpenComposer}>
              <Icon name="plus" />
              {t('lane.add')}
            </button>
          )}
        </div>
      </SortableContext>
    </section>
  )
}
