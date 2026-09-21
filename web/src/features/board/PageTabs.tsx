import { useDraggable, useDroppable } from '@dnd-kit/core'
import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { api, unwrap } from '../../api/client'
import { Icon } from '../../components/Icon'
import { InlineName } from '../../components/InlineName'
import { Menu } from '../../components/Menu'
import { useToast } from '../../components/Toast'
import { t, tn } from '../../i18n'
import { useQueryClient } from '@tanstack/react-query'
import { useMovePage, usePages } from '../hooks'
import type { Page } from '../types'
import { columnPlace, pageDropId, pageTabDragId, withoutColumn } from './dnd'

/** One tab: a link, a drop target for notes and columns, and (dragged) the way to reorder the pages. */
function Tab({ page, current, dropTarget, dragged, dropSide }: { page: Page; current: boolean; dropTarget: boolean; dragged: boolean; dropSide?: 'before' | 'after' }) {
  const drop = useDroppable({ id: pageDropId(page.id) })
  const drag = useDraggable({ id: pageTabDragId(page.id) })
  return (
    <Link
      ref={(el) => {
        drop.setNodeRef(el)
        drag.setNodeRef(el)
      }}
      to={`/p/${page.id}`}
      draggable={false} // the browser's own dragging of a link would take the mouse from the drag below
      {...drag.listeners}
      data-page={page.id}
      className={`page${current ? ' is-current' : ''}${dropTarget ? ' is-drop-target' : ''}${dragged ? ' is-page-dragged' : ''}${dropSide ? ` page-drop-${dropSide}` : ''}`}
      aria-current={current ? 'page' : undefined}
      title={page.name}
    >
      {page.name}
    </Link>
  )
}

interface Props {
  currentId: string | undefined
  /** The tab a dragged note or column is held over. */
  overPageId: string | null
  /** The tab being dragged to another place, and where it would land (before or after which tab). */
  draggedPageId?: string
  pageDrop?: { over: string; after: boolean } | null
}

/**
 * Page navigation. Every tab is also a drop target: hold a dragged note over one to open it (WEB-4). A tab can be
 * dragged to another place among the others, or moved with the page menu (WEB-23).
 */
export function PageTabs({ currentId, overPageId, draggedPageId, pageDrop }: Props) {
  const pages = usePages()
  const movePage = useMovePage()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const { toast } = useToast()
  const [adding, setAdding] = useState(false)
  const [renaming, setRenaming] = useState(false)
  const current = pages.data?.find((p) => p.id === currentId)
  const strip = useRef<HTMLDivElement>(null)

  // The strip has no scrollbar. The current tab is scrolled into view when the page changes (and once the tabs are
  // there), not whenever the list is fetched again: a refresh in the background must not throw the strip back
  // from where someone has scrolled it to, or from under a tab that is being dragged.
  const loaded = pages.isSuccess
  useEffect(() => {
    strip.current?.querySelector<HTMLElement>('[aria-current=page]')?.scrollIntoView?.({ inline: 'nearest', block: 'nearest' })
  }, [currentId, loaded, renaming])

  // The edges fade where more tabs are hidden.
  useEffect(() => {
    const el = strip.current
    if (!el) return
    const fade = () => {
      el.dataset.fadeStart = String(el.scrollLeft > 1)
      el.dataset.fadeEnd = String(el.scrollLeft + el.clientWidth < el.scrollWidth - 1)
    }
    fade()
    el.addEventListener('scroll', fade, { passive: true })
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(fade)
    observer?.observe(el)
    return () => {
      el.removeEventListener('scroll', fade)
      observer?.disconnect()
    }
  }, [pages.data, renaming])

  const refresh = () => void qc.invalidateQueries({ queryKey: ['pages'] })
  const create = async (name: string) => {
    setAdding(false)
    try {
      const page = unwrap(await api.POST('/api/v1/pages', { body: { id: crypto.randomUUID(), name } }))
      refresh()
      void navigate(`/p/${page.id}`)
    } catch {
      toast({ message: t('toast.failed') })
    }
  }
  // The visible tabs in order, and the menu's way of moving the current page one step.
  const tabs = (pages.data ?? []).filter((p) => !p.archived)
  const tabIds = tabs.map((p) => p.id)
  const at = current ? tabIds.indexOf(current.id) : -1
  const step = (dir: -1 | 1) => {
    if (!current) return
    const place = columnPlace(withoutColumn(tabIds, current.id), at + dir)
    movePage.mutate({ id: current.id, afterId: place.afterId, beforeId: place.beforeId })
  }
  const rename = async (name: string) => {
    setRenaming(false)
    if (!current) return
    try {
      unwrap(await api.PATCH('/api/v1/pages/{id}', { params: { path: { id: current.id } }, body: { name } }))
      refresh()
    } catch {
      toast({ message: t('toast.failed') })
    }
  }
  const remove = async () => {
    if (!current || !window.confirm(t('page.deleteConfirm', { name: current.name }))) return
    try {
      const res = unwrap(await api.DELETE('/api/v1/pages/{id}', { params: { path: { id: current.id } } }))
      void qc.invalidateQueries()
      if (res.moved_notes > 0) toast({ message: tn('toast.categoryDeleted', res.moved_notes) })
      const next = (pages.data ?? []).find((p) => p.id !== current.id)
      void navigate(next ? `/p/${next.id}` : '/')
    } catch {
      toast({ message: t('toast.failed') })
    }
  }

  return (
    <nav className="pages" aria-label={t('nav.pages')}>
      <div className="page-tabs" ref={strip}>
        {renaming && current ? (
          <InlineName initial={current.name} placeholder={t('page.namePlaceholder')} onCancel={() => setRenaming(false)} onSubmit={(n) => void rename(n)} />
        ) : (
          tabs.map((p) => (
            <Tab
              key={p.id}
              page={p}
              current={p.id === currentId}
              dropTarget={overPageId === p.id}
              dragged={draggedPageId === p.id}
              dropSide={pageDrop?.over === p.id ? (pageDrop.after ? 'after' : 'before') : undefined}
            />
          ))
        )}
      </div>
      <div className="page-tools">
        {current && !renaming && (
          <Menu
            label={t('nav.moreForPage')}
            items={[
              { label: t('page.rename'), onSelect: () => setRenaming(true) },
              ...(at > 0 ? [{ label: t('page.moveLeft'), onSelect: () => step(-1) }] : []),
              ...(at >= 0 && at < tabIds.length - 1 ? [{ label: t('page.moveRight'), onSelect: () => step(1) }] : []),
              { label: t('page.delete'), danger: true, onSelect: () => void remove() },
            ]}
            align="left"
          >
            <Icon name="more" />
          </Menu>
        )}
        {adding ? (
          <InlineName placeholder={t('page.namePlaceholder')} onCancel={() => setAdding(false)} onSubmit={(n) => void create(n)} />
        ) : (
          <button className="icon-btn" aria-label={t('nav.newPage')} title={t('nav.newPage')} onClick={() => setAdding(true)}>
            <Icon name="plus" />
          </button>
        )}
      </div>
    </nav>
  )
}
