import {
  DndContext, DragOverlay, KeyboardSensor, MouseSensor, closestCenter, pointerWithin, rectIntersection, useSensor, useSensors,
  type Announcements, type CollisionDetection, type DragEndEvent, type DragOverEvent, type DragStartEvent,
} from '@dnd-kit/core'
import { sortableKeyboardCoordinates } from '@dnd-kit/sortable'
import { useQueryClient } from '@tanstack/react-query'
import { useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { Navigate, Outlet, useNavigate, useParams } from 'react-router'
import { api, unwrap } from '../../api/client'
import { Icon } from '../../components/Icon'
import { TopbarSlot } from '../../components/topbar'
import { InlineName } from '../../components/InlineName'
import { useToast } from '../../components/Toast'
import { t } from '../../i18n'
import { neighbours } from '../cache'
import { useBoard, useInbox, useMoveNote, usePages } from '../hooks'
import { NoteCard } from '../notes/NoteCard'
import { INBOX, type Note } from '../types'
import {
  announceMoved, findLane, insertIndex, isLaneDrop, isPageDrop, isUnchanged, laneOf, moveAcross, pageOf, placementOf, reorder, type Arrangement,
} from './dnd'
import { Lane } from './Lane'
import { PageTabs } from './PageTabs'

const HOLD_TO_OPEN_MS = 650

interface LaneData {
  id: string
  name: string
  notes: Note[]
  total: number
  hasMore: boolean
}

/**
 * The board: page tabs, the Inbox tray and the columns of the current page, with drag and drop
 * (docs/design/07 sections 2 and 11). The drag context lives here, above the page route, so a
 * drag survives switching pages: hold a note over another page's tab and the board changes under it.
 */
export function BoardShell() {
  const { pageId } = useParams()
  const pages = usePages()
  const activePageId = pageId ?? pages.data?.find((p) => !p.archived)?.id
  const board = useBoard(activePageId)
  const inbox = useInbox()
  const move = useMoveNote()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const { toast } = useToast()

  const lanes: LaneData[] = useMemo(() => {
    const inboxNotes = inbox.data?.pages.flatMap((p) => p.items) ?? []
    const out: LaneData[] = [
      { id: INBOX, name: t('inbox.title'), notes: inboxNotes, total: board.data?.inbox_total ?? inbox.data?.pages[0]?.total ?? inboxNotes.length, hasMore: !!inbox.hasNextPage },
    ]
    for (const c of board.data?.categories ?? []) {
      out.push({ id: c.category.id, name: c.category.name, notes: c.notes, total: c.total, hasMore: !!c.next_cursor })
    }
    return out
  }, [inbox.data, inbox.hasNextPage, board.data])

  const noteById = useMemo(() => new Map(lanes.flatMap((l) => l.notes.map((n) => [n.id, n] as const))), [lanes])
  const names = useMemo(() => Object.fromEntries(lanes.map((l) => [l.id, l.name])), [lanes])
  const served: Arrangement = useMemo(() => Object.fromEntries(lanes.map((l) => [l.id, l.notes.map((n) => n.id)])), [lanes])

  // While dragging, the arrangement is local; otherwise it is what the server says.
  const [drag, setDrag] = useState<{ note: Note; arrangement: Arrangement; origin: ReturnType<typeof placementOf> } | null>(null)
  const arrangement = drag?.arrangement ?? served
  const [overPage, setOverPage] = useState<string | null>(null)
  const holdTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const lastOver = useRef<string | null>(null)

  const [composerLane, setComposerLane] = useState<string | null>(null)
  const [mobileLane, setMobileLane] = useState<string>(INBOX)
  const [addingColumn, setAddingColumn] = useState(false)

  // A stored lane that disappeared (deleted column, other page) falls back to the Inbox.
  const currentMobile = lanes.some((l) => l.id === mobileLane) ? mobileLane : INBOX

  const sensors = useSensors(
    // Mouse and keyboard only: on touch, dragging competes with scrolling, so phones use the Move-to menu (WEB-5).
    useSensor(MouseSensor, { activationConstraint: { distance: 5 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  )

  const collision: CollisionDetection = useCallback(
    (args) => {
      const pointer = pointerWithin(args)
      const hits = pointer.length > 0 ? pointer : rectIntersection(args)
      const first = hits[0]?.id
      if (first === undefined) return lastOver.current ? [{ id: lastOver.current }] : []
      let over = String(first)
      // Over a lane's body: pick the note nearest the pointer inside it, or the lane itself when empty.
      const pageHit = hits.find((h) => isPageDrop(String(h.id)))
      if (pageHit) over = String(pageHit.id)
      else if (isLaneDrop(over)) {
        const ids = arrangement[laneOf(over)] ?? []
        if (ids.length > 0) {
          const near = closestCenter({ ...args, droppableContainers: args.droppableContainers.filter((c) => ids.includes(String(c.id))) })[0]
          if (near) over = String(near.id)
        }
      }
      lastOver.current = over
      return [{ id: over }]
    },
    [arrangement],
  )

  const clearHold = () => {
    clearTimeout(holdTimer.current)
    holdTimer.current = undefined
    setOverPage(null)
  }

  const onDragStart = (e: DragStartEvent) => {
    const note = noteById.get(String(e.active.id))
    if (note) setDrag({ note, arrangement: served, origin: placementOf(served, note.id) })
  }

  const onDragOver = (e: DragOverEvent) => {
    if (!drag || !e.over) return
    const activeId = String(e.active.id)
    const overId = String(e.over.id)
    if (isPageDrop(overId)) {
      const page = pageOf(overId)
      setOverPage(page)
      if (!holdTimer.current && page !== activePageId) {
        holdTimer.current = setTimeout(() => {
          holdTimer.current = undefined
          void navigate(`/p/${page}`)
        }, HOLD_TO_OPEN_MS)
      }
      return
    }
    clearHold()
    const from = findLane(drag.arrangement, activeId)
    const toLane = isLaneDrop(overId) ? laneOf(overId) : findLane(drag.arrangement, overId)
    if (toLane === undefined) return
    if (from === toLane) return // sortable reorders inside a lane at drop time
    const ids = drag.arrangement[toLane] ?? []
    const overRect = e.over.rect
    const activeRect = e.active.rect.current.translated
    const below = !!activeRect && !isLaneDrop(overId) && activeRect.top + activeRect.height / 2 > overRect.top + overRect.height / 2
    const index = isLaneDrop(overId) ? ids.length : insertIndex(ids, overId, below)
    setDrag({ ...drag, arrangement: moveAcross(drag.arrangement, activeId, toLane, index) })
  }

  const onDragEnd = (e: DragEndEvent) => {
    clearHold()
    lastOver.current = null
    if (!drag) return
    const activeId = String(e.active.id)
    let arr = drag.arrangement
    const overId = e.over ? String(e.over.id) : null
    if (overId && !isPageDrop(overId) && !isLaneDrop(overId)) {
      const lane = findLane(arr, activeId)
      if (lane !== undefined && findLane(arr, overId) === lane) arr = reorder(arr, lane, activeId, overId)
    }
    const after = placementOf(arr, activeId)
    setDrag(null)
    if (!after || isUnchanged(drag.origin, after)) return
    const { afterId, beforeId } = neighbours(after.ids, after.index)
    move.mutate({ note: drag.note, categoryId: after.lane === INBOX ? null : after.lane, index: after.index, afterId, beforeId })
  }

  const onDragCancel = () => {
    clearHold()
    lastOver.current = null
    setDrag(null)
  }
  useEffect(() => () => clearTimeout(holdTimer.current), [])

  const announcements: Announcements = {
    onDragStart: () => t('dnd.lifted'),
    onDragOver: ({ active, over }) => {
      const arr = drag?.arrangement ?? served
      const lane = findLane(arr, String(active.id))
      if (!over || lane === undefined) return undefined
      if (isPageDrop(String(over.id))) return t('dnd.holdToOpen')
      const pos = arr[lane]!.indexOf(String(active.id))
      return announceMoved('over', names[lane] ?? '', pos, arr[lane]!.length)
    },
    onDragEnd: ({ active }) => {
      const arr = drag?.arrangement ?? served
      const lane = findLane(arr, String(active.id))
      if (lane === undefined) return undefined
      return announceMoved('drop', names[lane] ?? '', arr[lane]!.indexOf(String(active.id)), arr[lane]!.length)
    },
    onDragCancel: () => t('dnd.cancelled'),
  }

  const addColumn = async (name: string) => {
    setAddingColumn(false)
    if (!activePageId) return
    try {
      unwrap(await api.POST('/api/v1/categories', { body: { id: crypto.randomUUID(), page_id: activePageId, name } }))
      void qc.invalidateQueries({ queryKey: ['board'] })
      void qc.invalidateQueries({ queryKey: ['pages'] })
    } catch {
      toast({ message: t('toast.failed') })
    }
  }
  const renameColumn = async (id: string, name: string) => {
    try {
      unwrap(await api.PATCH('/api/v1/categories/{id}', { params: { path: { id } }, body: { name } }))
      void qc.invalidateQueries({ queryKey: ['board'] })
      void qc.invalidateQueries({ queryKey: ['pages'] })
    } catch {
      toast({ message: t('toast.failed') })
    }
  }
  const deleteColumn = async (lane: LaneData) => {
    if (!window.confirm(t('lane.deleteConfirm', { name: lane.name, count: lane.total }))) return
    try {
      const res = unwrap(await api.DELETE('/api/v1/categories/{id}', { params: { path: { id: lane.id } } }))
      void qc.invalidateQueries()
      toast({ message: t('toast.categoryDeleted', { count: res.moved_notes }) })
    } catch {
      toast({ message: t('toast.failed') })
    }
  }
  const showMoreInbox = () => void inbox.fetchNextPage()
  const showMoreLane = async (lane: LaneData) => {
    const cursor = board.data?.categories.find((c) => c.category.id === lane.id)?.next_cursor
    if (!cursor) return
    try {
      const page = unwrap(await api.GET('/api/v1/categories/{id}/notes', { params: { path: { id: lane.id }, query: { cursor, limit: 100 } } }))
      qc.setQueryData(['board', activePageId], (b: typeof board.data) =>
        b && {
          ...b,
          categories: b.categories.map((c) => (c.category.id === lane.id ? { ...c, notes: [...c.notes, ...page.items], next_cursor: page.next_cursor } : c)),
        },
      )
    } catch {
      toast({ message: t('toast.failed') })
    }
  }

  // Keyboard shortcuts (WEB-16): N opens the composer in the Inbox, / or Ctrl+K focuses search.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement
      if (target.matches('input, textarea, select, [contenteditable]') || e.ctrlKey || e.metaKey || e.altKey) return
      if (e.key === 'n') {
        e.preventDefault()
        setComposerLane(window.matchMedia('(max-width: 820px)').matches ? currentMobile : INBOX)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [currentMobile])
  useEffect(() => {
    const open = () => setComposerLane(window.matchMedia('(max-width: 820px)').matches ? currentMobile : INBOX)
    window.addEventListener('nk:new-note', open)
    return () => window.removeEventListener('nk:new-note', open)
  }, [currentMobile])

  if (pages.isSuccess && !pageId && pages.data.some((p) => !p.archived)) return <Navigate to={`/p/${pages.data.find((p) => !p.archived)!.id}`} replace />

  const noPages = pages.isSuccess && pages.data.filter((p) => !p.archived).length === 0
  const laneNodes = lanes.map((l) => {
    const ids = arrangement[l.id] ?? []
    const notes = ids.map((id) => noteById.get(id) ?? (drag?.note.id === id ? drag.note : undefined)).filter((n): n is Note => !!n)
    return (
      <Lane
        key={l.id}
        id={l.id}
        name={l.name}
        notes={notes}
        total={l.total}
        current={currentMobile === l.id}
        composerOpen={composerLane === l.id}
        onOpenComposer={() => setComposerLane(l.id)}
        onCloseComposer={() => setComposerLane(null)}
        hasMore={l.hasMore}
        onShowMore={l.id === INBOX ? showMoreInbox : () => void showMoreLane(l)}
        onRename={l.id === INBOX ? undefined : (n) => void renameColumn(l.id, n)}
        onDelete={l.id === INBOX ? undefined : () => void deleteColumn(l)}
      />
    )
  })

  return (
    <DndContext
      sensors={sensors}
      collisionDetection={collision}
      onDragStart={onDragStart}
      onDragOver={onDragOver}
      onDragEnd={onDragEnd}
      onDragCancel={onDragCancel}
      accessibility={{ announcements, screenReaderInstructions: { draggable: t('dnd.lifted') } }}
    >
      <BoardTopbar>
        <PageTabs currentId={activePageId} overPageId={overPage} />
      </BoardTopbar>
      <div className="tabs" role="tablist" aria-label={t('nav.pages')}>
        {lanes.map((l) => (
          <button key={l.id} role="tab" aria-selected={currentMobile === l.id} className={`tab${currentMobile === l.id ? ' is-current' : ''}`} onClick={() => setMobileLane(l.id)}>
            {l.name} <span className="count">{l.total}</span>
          </button>
        ))}
      </div>
      <div className="layout">
        {laneNodes[0]}
        <div className="board">
          {noPages ? (
            <div className="board-hint">
              <div>
                <strong>{t('page.first.title')}</strong>
                <p>{t('page.first.body')}</p>
              </div>
            </div>
          ) : (
            <>
              {laneNodes.slice(1)}
              {activePageId && (
                <div className="lane add-lane">
                  {addingColumn ? (
                    <InlineName placeholder={t('lane.columnPlaceholder')} onCancel={() => setAddingColumn(false)} onSubmit={(n) => void addColumn(n)} />
                  ) : (
                    <button className="add-row" onClick={() => setAddingColumn(true)}>
                      <Icon name="plus" />
                      {t('lane.addColumn')}
                    </button>
                  )}
                </div>
              )}
            </>
          )}
        </div>
      </div>
      <button className="fab" aria-label={t('nav.newNote')} onClick={() => setComposerLane(currentMobile)}>
        <Icon name="plus" />
      </button>
      <DragOverlay>{drag ? <NoteCard note={drag.note} overlay /> : null}</DragOverlay>
      <Outlet />
    </DndContext>
  )
}

/** Renders the page tabs into the top bar slot provided by the Layout. */
function BoardTopbar({ children }: { children: React.ReactNode }) {
  const slot = useContext(TopbarSlot)
  return slot ? createPortal(children, slot) : null
}
