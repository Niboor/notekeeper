import { useDroppable } from '@dnd-kit/core'
import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { api, unwrap } from '../../api/client'
import { Icon } from '../../components/Icon'
import { InlineName } from '../../components/InlineName'
import { Menu } from '../../components/Menu'
import { useToast } from '../../components/Toast'
import { t, tn } from '../../i18n'
import { useQueryClient } from '@tanstack/react-query'
import { usePages } from '../hooks'
import type { Page } from '../types'
import { pageDropId } from './dnd'

function Tab({ page, current, dropTarget }: { page: Page; current: boolean; dropTarget: boolean }) {
  const { setNodeRef } = useDroppable({ id: pageDropId(page.id) })
  return (
    <Link
      ref={setNodeRef}
      to={`/p/${page.id}`}
      className={`page${current ? ' is-current' : ''}${dropTarget ? ' is-drop-target' : ''}`}
      aria-current={current ? 'page' : undefined}
      title={page.name}
    >
      {page.name}
    </Link>
  )
}

/** Page navigation. Every tab is also a drop target: hold a dragged note over one to open it (WEB-4). */
export function PageTabs({ currentId, overPageId }: { currentId: string | undefined; overPageId: string | null }) {
  const pages = usePages()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const { toast } = useToast()
  const [adding, setAdding] = useState(false)
  const [renaming, setRenaming] = useState(false)
  const current = pages.data?.find((p) => p.id === currentId)
  const strip = useRef<HTMLDivElement>(null)

  // The strip has no scrollbar: the current tab is scrolled into view and the edges fade where more tabs are hidden.
  useEffect(() => {
    const el = strip.current
    if (!el) return
    el.querySelector<HTMLElement>('[aria-current=page]')?.scrollIntoView?.({ inline: 'nearest', block: 'nearest' })
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
  }, [currentId, pages.data, renaming])

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
          (pages.data ?? []).filter((p) => !p.archived).map((p) => <Tab key={p.id} page={p} current={p.id === currentId} dropTarget={overPageId === p.id} />)
        )}
      </div>
      <div className="page-tools">
        {current && !renaming && (
          <Menu label={t('nav.moreForPage')} items={[{ label: t('page.rename'), onSelect: () => setRenaming(true) }, { label: t('page.delete'), danger: true, onSelect: () => void remove() }]} align="left">
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
