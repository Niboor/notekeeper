import { useDraggable, useDroppable } from '@dnd-kit/core'
import { SortableContext, useSortable, verticalListSortingStrategy } from '@dnd-kit/sortable'
import { CSS } from '@dnd-kit/utilities'
import { useState } from 'react'
import { Icon } from '../../components/Icon'
import { InlineName } from '../../components/InlineName'
import { Menu } from '../../components/Menu'
import { t, tn } from '../../i18n'
import { Composer } from '../notes/Composer'
import { NoteCard } from '../notes/NoteCard'
import { INBOX, type Note } from '../types'
import { colDragId, colZoneId, laneDropId } from './dnd'

function SortableNote({ note, laneId, lifted, onKeyDown, siblings }: { note: Note; laneId: string; lifted: boolean; onKeyDown?: (e: React.KeyboardEvent, n: Note) => void; siblings: Note[] }) {
  const { listeners, setNodeRef, setActivatorNodeRef, transform, transition, isDragging } = useSortable({ id: note.id })
  return (
    <NoteCard
      note={note}
      laneId={laneId}
      siblings={siblings}
      dragging={isDragging}
      lifted={lifted}
      innerRef={(el) => {
        setNodeRef(el)
        setActivatorNodeRef(el)
      }}
      dragProps={{
        // Only the parts of dnd-kit's attributes that make sense here: the keyboard interaction is our own.
        'aria-roledescription': 'draggable note',
        'aria-describedby': 'dnd-instructions',
        tabIndex: 0,
        ...listeners,
        onKeyDown: (e) => onKeyDown?.(e, note),
        style: { transform: CSS.Translate.toString(transform), transition },
      }}
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
  liftedId?: string
  onNoteKeyDown?: (e: React.KeyboardEvent, n: Note) => void
  onShowMore?: () => void
  onRename?: (name: string) => void
  onDelete?: () => void
  /** What the column's menu offers for moving it: a step left or right (absent at the ends) and the other pages. */
  columnMoves?: { left?: () => void; right?: () => void; pages: { id: string; name: string }[]; toPage: (page: string) => void }
  /** While a column is dragged over this one: on which side of it the dragged column would land. */
  dropSide?: 'before' | 'after'
  /** This column is the one being dragged. */
  columnDragged?: boolean
}

/** One column: the Inbox or a category. Notes are draggable; an empty lane still accepts drops. */
export function Lane({
  id, name, notes, total, current, liftedId, onNoteKeyDown, composerOpen, onOpenComposer, onCloseComposer, hasMore, onShowMore, onRename, onDelete, columnMoves, dropSide, columnDragged,
}: Props) {
  const { setNodeRef, isOver } = useDroppable({ id: laneDropId(id) })
  const [renaming, setRenaming] = useState(false)
  const isInbox = id === INBOX
  // A column is dragged by its title (the Inbox stays where it is) and is dropped before or after another column.
  const grip = useDraggable({ id: colDragId(id), disabled: isInbox || renaming })
  const zone = useDroppable({ id: colZoneId(id), disabled: isInbox })

  return (
    <section
      ref={(el) => {
        setNodeRef(el)
        zone.setNodeRef(el)
        grip.setNodeRef(el)
      }}
      className={`lane${isInbox ? ' inbox' : ''}${current ? ' is-current' : ''}${isOver ? ' is-drop-target' : ''}${columnDragged ? ' is-column-dragged' : ''}${dropSide ? ` col-drop-${dropSide}` : ''}`}
      data-lane={id}
      aria-label={name}
    >
      <div className="lane-head">
        {renaming ? (
          <InlineName initial={name} placeholder={t('lane.columnPlaceholder')} onCancel={() => setRenaming(false)} onSubmit={(n) => (onRename?.(n), setRenaming(false))} />
        ) : (
          <div className={`lane-grip${isInbox ? '' : ' can-drag'}`} ref={isInbox ? undefined : grip.setActivatorNodeRef} {...(isInbox ? {} : grip.listeners)}>
            <h2>
              {isInbox && <Icon name="inbox" />}
              {name}
            </h2>
            <span className="count" aria-label={tn('lane.count', total)}>
              {total}
            </span>
          </div>
        )}
        {!isInbox && !renaming && (
          <Menu
            label={`${name}: ${t('nav.moreForPage')}`}
            className="lane-menu"
            items={[
              { label: t('lane.rename'), onSelect: () => setRenaming(true) },
              ...(columnMoves?.left ? [{ label: t('lane.moveLeft'), onSelect: columnMoves.left }] : []),
              ...(columnMoves?.right ? [{ label: t('lane.moveRight'), onSelect: columnMoves.right }] : []),
              ...(columnMoves && columnMoves.pages.length > 0
                ? [
                    { label: t('lane.moveToPage'), heading: true, onSelect: () => {} },
                    ...columnMoves.pages.map((p) => ({ label: p.name, onSelect: () => columnMoves.toPage(p.id) })),
                  ]
                : []),
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
            <SortableNote key={n.id} note={n} laneId={id} siblings={notes} lifted={liftedId === n.id} onKeyDown={onNoteKeyDown} />
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
