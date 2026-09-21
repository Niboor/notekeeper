import {
  DndContext, DragOverlay, MouseSensor, closestCenter, pointerWithin, rectIntersection, useDroppable, useSensor, useSensors,
  type CollisionDetection, type DragEndEvent, type DragMoveEvent, type DragOverEvent, type DragStartEvent,
} from '@dnd-kit/core'
import { useQueryClient } from '@tanstack/react-query'
import { useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { Navigate, Outlet, useNavigate, useParams, useSearchParams } from 'react-router'
import { api, unwrap } from '../../api/client'
import { Icon } from '../../components/Icon'
import { TopbarSlot } from '../../components/topbar'
import { InlineName } from '../../components/InlineName'
import { useToast } from '../../components/Toast'
import { t, tn } from '../../i18n'
import { neighbours } from '../cache'
import { useBoard, useInbox, useMoreNotes, useMoveColumn, useMoveNote, usePages, type MoveColumnVars } from '../hooks'
import { NoteCard } from '../notes/NoteCard'
import { INBOX, type Note } from '../types'
import {
  colOf, colZoneId, columnDrop, columnPlace, columnUnchanged, findLane, insertIndex, isColDrag, isColZone, isLaneDrop, isPageDrop, isUnchanged, keyboardStep, laneOf,
  moveAcross, pageOf, placementOf, rebase, reorder, withoutColumn, type Arrangement, type ColumnDrop,
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

const NO_CURSORS: Record<string, string[]> = {}

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
  // The pages loaded with "show more" belong to the page they were asked on; another page starts again.
  const [more, setMore] = useState<{ page?: string; cursors: Record<string, string[]> }>({ cursors: {} })
  const moreCursors = more.page === activePageId ? more.cursors : NO_CURSORS
  const moreNotes = useMoreNotes(activePageId, moreCursors)
  const inbox = useInbox()
  const move = useMoveNote()
  const moveColumn = useMoveColumn()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const { toast } = useToast()
  const [search] = useSearchParams()
  const target = search.get('note')

  const lanes: LaneData[] = useMemo(() => {
    const inboxNotes = inbox.data?.pages.flatMap((p) => p.items) ?? []
    const out: LaneData[] = [
      { id: INBOX, name: t('inbox.title'), notes: inboxNotes, total: board.data?.inbox_total ?? inbox.data?.pages[0]?.total ?? inboxNotes.length, hasMore: !!inbox.hasNextPage },
    ]
    for (const c of board.data?.categories ?? []) {
      // Pages loaded with "show more" are appended, and survive every refetch of the board (CR-052).
      const extra = moreNotes[c.category.id]
      const have = new Set(c.notes.map((n) => n.id))
      const notes = extra ? [...c.notes, ...extra.notes.filter((n) => !have.has(n.id))] : c.notes
      const hasMore = extra ? extra.next !== null : !!c.next_cursor
      out.push({ id: c.category.id, name: c.category.name, notes, total: c.total, hasMore })
    }
    return out
  }, [inbox.data, inbox.hasNextPage, board.data, moreNotes])

  const noteById = useMemo(() => new Map(lanes.flatMap((l) => l.notes.map((n) => [n.id, n] as const))), [lanes])
  const names = useMemo(() => Object.fromEntries(lanes.map((l) => [l.id, l.name])), [lanes])
  const served: Arrangement = useMemo(() => Object.fromEntries(lanes.map((l) => [l.id, l.notes.map((n) => n.id)])), [lanes])

  // While dragging, the arrangement is local; otherwise it is what the server says.
  // A link to a note (from a reminder) lands with that card in view and focused.
  useEffect(() => {
    if (!target) return
    const el = document.querySelector<HTMLElement>(`article.note[data-id="${CSS.escape(target)}"]`)
    if (el) {
      el.scrollIntoView({ block: 'center' })
      el.focus()
    }
  }, [target, lanes])

  const [drag, setDrag] = useState<{ note: Note; arrangement: Arrangement; origin: ReturnType<typeof placementOf> } | null>(null)
  // The lanes on screen can change during a drag (another page opened by holding over its tab): rebase onto them.
  const [lift, setLift] = useState<{ note: Note; origin: ReturnType<typeof placementOf>; arrangement: Arrangement } | null>(null)
  const [announcement, setAnnouncement] = useState('')
  // A column being dragged by its title: where it came from and which column it is over, on which side.
  const [colDrag, setColDrag] = useState<{ id: string; name: string; total: number; originPage: string; originIndex: number; target: { over: string; after: boolean } | null } | null>(null)
  const arrangement = drag ? rebase(drag.arrangement, served, drag.note.id) : lift ? rebase(lift.arrangement, served, lift.note.id) : served
  const [overPage, setOverPage] = useState<string | null>(null)
  const holdTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const lastOver = useRef<string | null>(null)

  const [composerLane, setComposerLane] = useState<string | null>(null)
  const [mobileLane, setMobileLane] = useState<string>(INBOX)
  const [addingColumn, setAddingColumn] = useState(false)

  // A stored lane that disappeared (deleted column, other page) falls back to the Inbox.
  const currentMobile = lanes.some((l) => l.id === mobileLane) ? mobileLane : INBOX

  const sensors = useSensors(
    // The mouse only: on touch, dragging competes with scrolling, so phones use the Move-to menu, and
    // the keyboard has its own lift-and-move interaction below (WEB-5).
    useSensor(MouseSensor, { activationConstraint: { distance: 5 } }),
  )

  const collision: CollisionDetection = useCallback(
    (args) => {
      const pointer = pointerWithin(args)
      if (isColDrag(String(args.active.id))) {
        // A column goes on a page tab, or before or after a column (the nearest one when the pointer is between them).
        const tab = pointer.find((h) => isPageDrop(String(h.id)))
        const zone = tab ?? pointer.find((h) => isColZone(String(h.id))) ?? closestCenter({ ...args, droppableContainers: args.droppableContainers.filter((c) => isColZone(String(c.id))) })[0]
        if (!zone) return lastOver.current ? [{ id: lastOver.current }] : []
        lastOver.current = String(zone.id)
        return [{ id: zone.id }]
      }
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
  /** Holding a dragged note or column over another page's tab opens that page, so it can be dropped there (WEB-4). */
  const holdOverPage = (page: string) => {
    setOverPage(page)
    if (!holdTimer.current && page !== activePageId) {
      holdTimer.current = setTimeout(() => {
        holdTimer.current = undefined
        void navigate(`/p/${page}`)
      }, HOLD_TO_OPEN_MS)
    }
  }

  // ---- columns: the ids of this page's columns in order, and moving one (by drag or by the menu) ----------
  const columnIds = useMemo(() => lanes.filter((l) => l.id !== INBOX).map((l) => l.id), [lanes])
  const pageName = (id: string) => pages.data?.find((p) => p.id === id)?.name ?? ''
  /** Puts a column at a place on a page. `from` says where it was, for the message's Undo. */
  const placeColumnOn = (from: { id: string; name: string; originPage: string; originIndex: number }, toPage: string, drop: ColumnDrop) => {
    if (columnUnchanged(from.originPage, from.originIndex, toPage, drop)) return
    // Where it was, among the columns of its own page: only known while that page is on screen.
    const back = from.originPage === activePageId ? columnPlace(withoutColumn(columnIds, from.id), from.originIndex) : columnPlace([], 9999)
    const vars: MoveColumnVars = {
      categoryId: from.id, name: from.name, fromPage: from.originPage, toPage, toPageName: pageName(toPage),
      index: drop.index, afterId: drop.afterId, beforeId: drop.beforeId,
      undo: { categoryId: from.id, name: from.name, fromPage: toPage, toPage: from.originPage, toPageName: pageName(from.originPage), index: back.index, afterId: back.afterId, beforeId: back.beforeId },
    }
    moveColumn.mutate(vars)
  }
  const columnMovesFor = (l: LaneData) => {
    if (l.id === INBOX || !activePageId) return undefined
    const i = columnIds.indexOf(l.id)
    const from = { id: l.id, name: l.name, originPage: activePageId, originIndex: i }
    const others = withoutColumn(columnIds, l.id)
    return {
      left: i > 0 ? () => placeColumnOn(from, activePageId, columnPlace(others, i - 1)) : undefined,
      right: i >= 0 && i < columnIds.length - 1 ? () => placeColumnOn(from, activePageId, columnPlace(others, i + 1)) : undefined,
      pages: (pages.data ?? []).filter((p) => !p.archived && p.id !== activePageId).map((p) => ({ id: p.id, name: p.name })),
      toPage: (page: string) => placeColumnOn(from, page, columnPlace([], 9999)),
    }
  }

  const onDragStart = (e: DragStartEvent) => {
    if (isColDrag(String(e.active.id))) {
      const id = colOf(String(e.active.id))
      const lane = lanes.find((l) => l.id === id)
      if (lane && activePageId) setColDrag({ id, name: lane.name, total: lane.total, originPage: activePageId, originIndex: columnIds.indexOf(id), target: null })
      return
    }
    const note = noteById.get(String(e.active.id))
    if (note) setDrag({ note, arrangement: served, origin: placementOf(served, note.id) })
  }

  const onDragOver = (e: DragOverEvent) => {
    if (colDrag) {
      if (!e.over) return
      const overId = String(e.over.id)
      if (isPageDrop(overId)) {
        holdOverPage(pageOf(overId))
        if (colDrag.target) setColDrag({ ...colDrag, target: null })
        return
      }
      clearHold()
      return
    }
    if (!drag || !e.over) return
    const activeId = String(e.active.id)
    const overId = String(e.over.id)
    if (isPageDrop(overId)) {
      holdOverPage(pageOf(overId))
      return
    }
    clearHold()
    const current = rebase(drag.arrangement, served, activeId)
    const from = findLane(current, activeId)
    const toLane = isLaneDrop(overId) ? laneOf(overId) : findLane(current, overId)
    if (toLane === undefined) return
    if (from === toLane) {
      if (current !== drag.arrangement) setDrag({ ...drag, arrangement: current })
      return // sortable reorders inside a lane at drop time
    }
    const ids = current[toLane] ?? []
    const overRect = e.over.rect
    const activeRect = e.active.rect.current.translated
    const below = !!activeRect && !isLaneDrop(overId) && activeRect.top + activeRect.height / 2 > overRect.top + overRect.height / 2
    const index = isLaneDrop(overId) ? ids.length : insertIndex(ids, overId, below)
    setDrag({ ...drag, arrangement: moveAcross(current, activeId, toLane, index) })
  }

  /** While a column is dragged over another: on which side of it it would land, followed as the pointer moves. */
  const onDragMove = (e: DragMoveEvent) => {
    if (!colDrag) return
    const overId = e.over ? String(e.over.id) : ''
    const next = isColZone(overId) && colOf(overId) !== colDrag.id ? { over: colOf(overId), after: isRightHalf(pointerX(e), colOf(overId)) } : null
    if (colDrag.target?.over !== next?.over || colDrag.target?.after !== next?.after) setColDrag({ ...colDrag, target: next })
  }

  const onDragEnd = (e: DragEndEvent) => {
    clearHold()
    lastOver.current = null
    if (colDrag) {
      const dragged = colDrag
      setColDrag(null)
      const overId = e.over ? String(e.over.id) : null
      if (!overId || !activePageId) return
      if (isPageDrop(overId)) {
        // Dropped on a page's tab: the column goes to the end of that page.
        if (pageOf(overId) !== activePageId) placeColumnOn(dragged, pageOf(overId), columnPlace([], 0))
        return
      }
      if (!isColZone(overId) || colOf(overId) === dragged.id) return
      const others = withoutColumn(columnIds, dragged.id)
      placeColumnOn(dragged, activePageId, columnDrop(others, colOf(overId), isRightHalf(pointerX(e), colOf(overId))))
      return
    }
    if (!drag) return
    const activeId = String(e.active.id)
    let arr = rebase(drag.arrangement, served, activeId)
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
    setColDrag(null)
  }
  useEffect(() => () => clearTimeout(holdTimer.current), [])

  // ---- keyboard: Space lifts a focused note, the arrows move it, Space drops it, Escape cancels ----------

  const laneOrder = useMemo(() => lanes.map((l) => l.id), [lanes])
  const say = (key: Parameters<typeof t>[0], vars?: Record<string, string | number>) => setAnnouncement(t(key, vars))
  const laneNameFor = (arr: Arrangement, id: string) => names[findLane(arr, id) ?? ''] ?? ''

  const onNoteKeyDown = (e: React.KeyboardEvent, note: Note) => {
    if (e.target !== e.currentTarget) return // typing in an editor, or a button, is not a drag
    if (!lift) {
      if (e.key === ' ') {
        e.preventDefault()
        setLift({ note, origin: placementOf(served, note.id), arrangement: served })
        say('dnd.lifted')
      }
      return
    }
    if (lift.note.id !== note.id) return
    const current = rebase(lift.arrangement, served, note.id)
    if (e.key === 'Escape') {
      e.preventDefault()
      setLift(null)
      say('dnd.cancelled')
      return
    }
    if (e.key === ' ') {
      e.preventDefault()
      const after = placementOf(current, note.id)
      setLift(null)
      if (!after) return
      say('dnd.dropped', { lane: laneNameFor(current, note.id), pos: after.index + 1, total: after.ids.length })
      if (isUnchanged(lift.origin, after)) return
      const { afterId, beforeId } = neighbours(after.ids, after.index)
      move.mutate({ note: lift.note, categoryId: after.lane === INBOX ? null : after.lane, index: after.index, afterId, beforeId })
      return
    }
    const next = keyboardStep(current, laneOrder, note.id, e.key)
    if (e.key.startsWith('Arrow')) e.preventDefault()
    if (next) {
      setLift({ ...lift, arrangement: next })
      const p = placementOf(next, note.id)!
      say('dnd.movedTo', { lane: laneNameFor(next, note.id), pos: p.index + 1, total: p.ids.length })
    }
  }
  // The card is rebuilt in its new lane after a move; put the focus back on it.
  useEffect(() => {
    if (lift) document.querySelector<HTMLElement>(`article.note[data-id="${lift.note.id}"]`)?.focus()
  }, [lift])
  // Leaving the card (clicking elsewhere) drops the lift without moving anything.
  useEffect(() => {
    if (!lift) return
    const cancel = (e: FocusEvent) => {
      const el = document.querySelector(`article.note[data-id="${lift.note.id}"]`)
      if (!el || (e.target as Node) === el) return
      if (!el.contains(e.target as Node)) setLift(null)
    }
    document.addEventListener('focusin', cancel)
    return () => document.removeEventListener('focusin', cancel)
  }, [lift])

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
      toast({ message: tn('toast.categoryDeleted', res.moved_notes) })
    } catch {
      toast({ message: t('toast.failed') })
    }
  }
  const showMoreInbox = () => void inbox.fetchNextPage()
  const showMoreLane = (lane: LaneData) => {
    const extra = moreNotes[lane.id]
    if (extra && extra.next === undefined) return // the last page asked for is still loading
    const cursor = extra ? extra.next : board.data?.categories.find((c) => c.category.id === lane.id)?.next_cursor
    if (!cursor) return
    setMore((m) => {
      const cur = m.page === activePageId ? m.cursors : NO_CURSORS
      return { page: activePageId, cursors: { ...cur, [lane.id]: [...(cur[lane.id] ?? []), cursor] } }
    })
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
        liftedId={lift?.note.id}
        onNoteKeyDown={onNoteKeyDown}
        onOpenComposer={() => setComposerLane(l.id)}
        onCloseComposer={() => setComposerLane(null)}
        hasMore={l.hasMore}
        onShowMore={l.id === INBOX ? showMoreInbox : () => showMoreLane(l)}
        onRename={l.id === INBOX ? undefined : (n) => void renameColumn(l.id, n)}
        onDelete={l.id === INBOX ? undefined : () => void deleteColumn(l)}
        columnMoves={columnMovesFor(l)}
        columnDragged={colDrag?.id === l.id}
        dropSide={colDrag?.target?.over === l.id ? (colDrag.target.after ? 'after' : 'before') : undefined}
      />
    )
  })

  return (
    <DndContext
      sensors={sensors}
      collisionDetection={collision}
      onDragStart={onDragStart}
      onDragOver={onDragOver}
      onDragMove={onDragMove}
      onDragEnd={onDragEnd}
      onDragCancel={onDragCancel}
      // Auto-scrolling belongs to the board and its columns; the page tabs stay where they are while a note or column is held over one.
      autoScroll={{ canScroll: (el) => !el.closest('.topbar') }}
      accessibility={{ screenReaderInstructions: { draggable: t('dnd.instructions') } }}
    >
      <div className="sr-only" role="status" aria-live="assertive" aria-atomic="true" data-testid="dnd-announcer">
        {announcement}
      </div>
      <div id="dnd-instructions" className="sr-only">
        {t('dnd.instructions')}
      </div>
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
                <AddColumn
                  adding={addingColumn}
                  onStart={() => setAddingColumn(true)}
                  onCancel={() => setAddingColumn(false)}
                  onSubmit={(n) => void addColumn(n)}
                  dropHere={colDrag?.target?.over === END_ZONE}
                />
              )}
            </>
          )}
        </div>
      </div>
      <button className="fab" aria-label={t('nav.newNote')} onClick={() => setComposerLane(currentMobile)}>
        <Icon name="plus" />
      </button>
      <DragOverlay>
        {colDrag ? (
          <div className="column-preview">
            <strong>{colDrag.name}</strong> <span className="count">{colDrag.total}</span>
          </div>
        ) : drag ? (
          <NoteCard note={drag.note} overlay />
        ) : null}
      </DragOverlay>
      <Outlet />
    </DndContext>
  )
}

/** Renders the page tabs into the top bar slot provided by the Layout. */
function BoardTopbar({ children }: { children: React.ReactNode }) {
  const slot = useContext(TopbarSlot)
  return slot ? createPortal(children, slot) : null
}

/** The id of the drop zone after the last column: dropping a column there puts it at the end of the page. */
const END_ZONE = 'end'

/** Where the pointer is, horizontally, during a drag: where it went down plus how far it has moved. */
function pointerX(e: { activatorEvent: Event; delta: { x: number } }): number {
  return ((e.activatorEvent as MouseEvent).clientX ?? 0) + e.delta.x
}

/**
 * True when the pointer is in the right half of the column (or the end tile) it is over. The column is measured
 * now, not when the drag began: the board scrolls sideways during a drag, and a rect taken at the start is then stale.
 */
function isRightHalf(pointer: number, column: string): boolean {
  const el = document.querySelector(column === END_ZONE ? '.add-lane' : `section[data-lane="${CSS.escape(column)}"]`)
  const box = el?.getBoundingClientRect()
  return !!box && pointer > box.left + box.width / 2
}

/** The tile after the last column: adds a column, and is where a dragged column can be dropped to go last (or onto an empty page). */
function AddColumn({ adding, onStart, onCancel, onSubmit, dropHere }: { adding: boolean; onStart: () => void; onCancel: () => void; onSubmit: (name: string) => void; dropHere: boolean }) {
  const { setNodeRef } = useDroppable({ id: colZoneId(END_ZONE) })
  return (
    <div ref={setNodeRef} className={`lane add-lane${dropHere ? ' col-drop-before' : ''}`}>
      {adding ? (
        <InlineName placeholder={t('lane.columnPlaceholder')} onCancel={onCancel} onSubmit={onSubmit} />
      ) : (
        <button className="add-row" onClick={onStart}>
          <Icon name="plus" />
          {t('lane.addColumn')}
        </button>
      )}
    </div>
  )
}
