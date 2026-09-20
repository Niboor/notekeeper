import { useInfiniteQuery, useMutation, useQueries, useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query'
import { useCallback, useRef } from 'react'
import { api, ApiError, unwrap, unwrapEmpty } from '../api/client'
import { CLIENT_HEADER, refreshSession } from '../api/session'
import { useToast } from '../components/Toast'
import { t } from '../i18n'
import {
  insertIntoBoard, insertIntoInbox, removeFromBoard, removeFromInbox, replaceInInbox, replaceOnBoard, withInboxTotal,
  type InboxData,
} from './cache'
import type { Board, Note } from './types'

// ---- queries --------------------------------------------------------------------------------

export function usePages() {
  return useQuery({ queryKey: ['pages'], queryFn: async () => unwrap(await api.GET('/api/v1/pages')).items })
}

export function useBoard(pageId: string | undefined) {
  return useQuery({
    queryKey: ['board', pageId],
    enabled: !!pageId,
    queryFn: async () =>
      unwrap(await api.GET('/api/v1/pages/{id}/board', { params: { path: { id: pageId! }, query: { notes_per_category: 100 } } })),
  })
}

/** The board queries themselves: ['board', pageId]. Longer keys under 'board' are the extra pages of a column. */
const boardsOnly = { queryKey: ['board'], predicate: (q: { queryKey: readonly unknown[] }) => q.queryKey.length === 2 }

export interface MoreNotes {
  notes: Note[]
  /** Cursor for the page after the last one loaded; null when the column is complete; undefined while loading. */
  next: string | null | undefined
}

/**
 * The pages a person asked for with "show more", per column. They are queries under the board's key, so
 * every change event refetches them together with the board instead of throwing them away (CR-052).
 */
export function useMoreNotes(pageId: string | undefined, cursors: Record<string, string[]>): Record<string, MoreNotes> {
  const entries = Object.entries(cursors).flatMap(([category, list]) => list.map((cursor) => ({ category, cursor })))
  const signature = JSON.stringify(entries) // a stable identity, so the combined result only changes when it must
  const combine = useCallback(
    (results: { data?: { items: Note[]; next_cursor?: string | null } }[]) => {
      const out: Record<string, MoreNotes> = {}
      ;(JSON.parse(signature) as typeof entries).forEach((e, i) => {
        const o = (out[e.category] ??= { notes: [], next: undefined })
        const data = results[i]?.data
        if (data) {
          o.notes.push(...data.items)
          o.next = data.next_cursor ?? null
        } else o.next = undefined
      })
      return out
    },
    [signature],
  )
  return useQueries({
    queries: entries.map((e) => ({
      queryKey: ['board', pageId, 'more', e.category, e.cursor],
      enabled: !!pageId,
      queryFn: async () =>
        unwrap(await api.GET('/api/v1/categories/{id}/notes', { params: { path: { id: e.category }, query: { cursor: e.cursor, limit: 100 } } })),
    })),
    combine,
  })
}

export function useInbox() {
  return useInfiniteQuery({
    queryKey: ['inbox'],
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) =>
      unwrap(await api.GET('/api/v1/inbox/notes', { params: { query: { cursor: pageParam, limit: 50 } } })),
    getNextPageParam: (last) => last.next_cursor,
  })
}

export function useTrash() {
  return useInfiniteQuery({
    queryKey: ['trash'],
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => unwrap(await api.GET('/api/v1/trash/notes', { params: { query: { cursor: pageParam, limit: 50 } } })),
    getNextPageParam: (last) => last.next_cursor,
  })
}

// ---- optimistic plumbing ---------------------------------------------------------------------

interface Snapshot {
  boards: [readonly unknown[], Board | undefined][]
  inbox: InboxData | undefined
}

/** Captures the cached views without waiting for anything. */
function capture(qc: QueryClient): Snapshot {
  return { boards: qc.getQueriesData<Board>(boardsOnly), inbox: qc.getQueryData<InboxData>(['inbox']) }
}

/**
 * A mutation whose effect is shown on screen before the server answers. `apply` changes the cached
 * views. It runs twice: at once, so that the very next frame (a ticked
 * checkbox, a dropped card) already shows the result, and again after in-flight refetches have been
 * cancelled, because cancelling reverts a query to its earlier state and would otherwise undo it.
 */
function useOptimistic<V, R>(o: {
  mutationFn: (v: V) => Promise<R>
  apply: (qc: QueryClient, v: V) => void
  onSuccess?: (data: R, v: V) => void
  onSettled?: () => void
}) {
  const qc = useQueryClient()
  const { toast } = useToast()
  const pending = useRef<Snapshot | undefined>(undefined)
  const m = useMutation<R, Error, V, Snapshot | undefined>({
    mutationFn: o.mutationFn,
    onMutate: async (v) => {
      const snap = pending.current
      await qc.cancelQueries({ queryKey: ['board'] })
      await qc.cancelQueries({ queryKey: ['inbox'] })
      // Start again from the captured state so that applying twice cannot double-count anything.
      rollback(qc, snap)
      o.apply(qc, v)
      return snap
    },
    onSuccess: o.onSuccess,
    onError: (_e, _v, snap) => {
      rollback(qc, snap)
      toast({ message: t('toast.failed') })
    },
    onSettled: () => {
      refresh(qc)
      o.onSettled?.()
    },
  })
  const mutate = (v: V, opts?: { onSuccess?: (data: R) => void; onError?: () => void }) => {
    pending.current = capture(qc)
    o.apply(qc, v)
    m.mutate(v, opts)
  }
  return { ...m, mutate }
}

function rollback(qc: QueryClient, s: Snapshot | undefined) {
  if (!s) return
  for (const [key, data] of s.boards) qc.setQueryData(key, data)
  qc.setQueryData(['inbox'], s.inbox)
}

function refresh(qc: QueryClient) {
  void qc.invalidateQueries({ queryKey: ['board'] })
  void qc.invalidateQueries({ queryKey: ['inbox'] })
  void qc.invalidateQueries({ queryKey: ['pages'] })
}

function mapBoards(qc: QueryClient, fn: (b: Board) => Board) {
  for (const [key, data] of qc.getQueriesData<Board>(boardsOnly)) {
    if (data) qc.setQueryData(key, fn(data))
  }
}

/** Takes a note out of every cached view. Returns whether it was an Inbox note, which changes the Inbox count. */
function detach(qc: QueryClient, note: Note) {
  mapBoards(qc, (b) => {
    let out = removeFromBoard(b, note.id)
    if (!note.category_id) out = withInboxTotal(out, -1)
    return out
  })
  qc.setQueryData<InboxData | undefined>(['inbox'], (d) => removeFromInbox(d, note.id))
}

// ---- moving ----------------------------------------------------------------------------------

export interface MoveVars {
  note: Note
  /** null moves the note to the Inbox. */
  categoryId: string | null
  /** Where in the target column it lands, for the optimistic view. */
  index: number
  afterId?: string | null
  beforeId?: string | null
}

function applyMove(qc: QueryClient, v: MoveVars) {
  const moved = { ...v.note, category_id: v.categoryId }
  mapBoards(qc, (b) => {
    let out = removeFromBoard(b, v.note.id)
    if (!v.note.category_id) out = withInboxTotal(out, -1)
    if (v.categoryId) out = insertIntoBoard(out, v.categoryId, moved, v.index)
    else out = withInboxTotal(out, 1)
    return out
  })
  qc.setQueryData<InboxData | undefined>(['inbox'], (d) => {
    const without = removeFromInbox(d, v.note.id)
    return v.categoryId ? without : insertIntoInbox(without, moved)
  })
}

export function useMoveNote() {
  return useOptimistic<MoveVars, Note>({
    mutationFn: async (v) =>
      unwrap(
        await api.POST('/api/v1/notes/{id}/move', {
          params: { path: { id: v.note.id } },
          body: { category_id: v.categoryId, after_id: v.afterId ?? null, before_id: v.beforeId ?? null },
        }),
      ),
    apply: applyMove,
  })
}

// ---- dismiss, restore, delete ---------------------------------------------------------------

export function useRestoreNote() {
  const qc = useQueryClient()
  const { toast } = useToast()
  return useMutation({
    mutationFn: async (id: string) => unwrap(await api.POST('/api/v1/notes/{id}/restore', { params: { path: { id } } })),
    onError: () => toast({ message: t('toast.failed') }),
    onSettled: () => {
      refresh(qc)
      void qc.invalidateQueries({ queryKey: ['trash'] })
    },
  })
}

/** Dismisses a note at once (no confirmation) and offers Undo for 10 seconds (WEB-6, CORE-N8). */
export function useDismissNote() {
  const qc = useQueryClient()
  const { toast } = useToast()
  const restore = useRestoreNote()
  return useOptimistic<Note, Note>({
    mutationFn: async (note) => unwrap(await api.POST('/api/v1/notes/{id}/dismiss', { params: { path: { id: note.id } } })),
    apply: (c, note) => detach(c, note),
    onSuccess: (_d, note) => {
      toast({ message: t('toast.dismissed'), action: { label: t('toast.undo'), onClick: () => restore.mutate(note.id) } })
    },
    onSettled: () => void qc.invalidateQueries({ queryKey: ['trash'] }),
  })
}

export function useDeleteForever() {
  const qc = useQueryClient()
  const { toast } = useToast()
  return useMutation({
    mutationFn: async (id: string) => unwrapEmpty(await api.DELETE('/api/v1/notes/{id}', { params: { path: { id } } })),
    onSuccess: () => toast({ message: t('toast.dismissedDeletedForever') }),
    onError: () => toast({ message: t('toast.failed') }),
    onSettled: () => void qc.invalidateQueries({ queryKey: ['trash'] }),
  })
}

// ---- creating and editing ------------------------------------------------------------------

export interface CreateVars {
  /** null creates the note in the Inbox. */
  categoryId: string | null
  text: string
  attachmentIds: string[]
}

function applyCreate(qc: QueryClient, v: CreateVars & { id: string }) {
  const now = new Date().toISOString()
  const temp: Note = {
    id: v.id, category_id: v.categoryId, state: 'active', created_at: now, updated_at: now, version: 1,
    parts: v.text.trim() ? [{ id: v.id + '-t', kind: 'text', text: v.text, attach_reason: 'app', created_at: now }] : [],
  }
  if (temp.parts.length === 0) return // attachments only: it appears when the server answers
  // Idempotent: applying twice must not show two copies.
  mapBoards(qc, (b) => {
    const clean = removeFromBoard(b, v.id)
    return v.categoryId ? insertIntoBoard(clean, v.categoryId, temp, 0) : withInboxTotal(clean, clean === b ? 1 : 0)
  })
  if (!v.categoryId) qc.setQueryData<InboxData | undefined>(['inbox'], (d) => insertIntoInbox(removeFromInbox(d, v.id), temp))
}

export function useCreateNote() {
  return useOptimistic<CreateVars & { id: string }, Note>({
    mutationFn: async (v) => {
      const parts: { type: 'text' | 'attachment'; text?: string; attachment_id?: string }[] = []
      if (v.text.trim()) parts.push({ type: 'text', text: v.text })
      for (const a of v.attachmentIds) parts.push({ type: 'attachment', attachment_id: a })
      return unwrap(await api.POST('/api/v1/notes', { body: { id: v.id, category_id: v.categoryId, parts } }))
    },
    apply: applyCreate,
  })
}

export function useEditPart() {
  return useOptimistic<{ note: Note; partId: string; text: string }, { note: Note; stale: boolean }>({
    mutationFn: async (v) =>
      unwrap(
        await api.PATCH('/api/v1/notes/{id}/parts/{partId}', {
          params: { path: { id: v.note.id, partId: v.partId } },
          body: { text: v.text, base_version: v.note.version },
        }),
      ),
    apply: (qc, v) => {
      const edited: Note = { ...v.note, parts: v.note.parts.map((p) => (p.id === v.partId ? { ...p, text: v.text } : p)) }
      mapBoards(qc, (b) => replaceOnBoard(b, edited))
      qc.setQueryData<InboxData | undefined>(['inbox'], (d) => replaceInInbox(d, edited))
    },
  })
}

export function useAddPart() {
  const qc = useQueryClient()
  const { toast } = useToast()
  return useMutation({
    mutationFn: async (v: { noteId: string; text?: string; attachmentId?: string }) =>
      unwrap(
        await api.POST('/api/v1/notes/{id}/parts', {
          params: { path: { id: v.noteId } },
          body: v.attachmentId ? { type: 'attachment', attachment_id: v.attachmentId } : { type: 'text', text: v.text },
        }),
      ),
    onError: () => toast({ message: t('toast.failed') }),
    onSettled: () => refresh(qc),
  })
}

export function useRemovePart() {
  const qc = useQueryClient()
  const { toast } = useToast()
  return useMutation({
    mutationFn: async (v: { noteId: string; partId: string }) =>
      unwrap(await api.DELETE('/api/v1/notes/{id}/parts/{partId}', { params: { path: { id: v.noteId, partId: v.partId } } })),
    onError: () => toast({ message: t('toast.failed') }),
    onSettled: () => refresh(qc),
  })
}

// ---- attachments -----------------------------------------------------------------------------

export interface UploadedFile {
  id: string
  filename: string
  media_type: string
  size: number
}

// Uploads go through a small queue: the server lets one person run only a few at once (429 too_many_uploads),
// so choosing ten photos must not turn six of them into failures (CR-051).
const MAX_PARALLEL_UPLOADS = 3
let activeUploads = 0
const waitingUploads: (() => void)[] = []

async function withUploadSlot<T>(run: () => Promise<T>): Promise<T> {
  if (activeUploads >= MAX_PARALLEL_UPLOADS) await new Promise<void>((resolve) => waitingUploads.push(resolve))
  activeUploads++
  try {
    return await run()
  } finally {
    activeUploads--
    waitingUploads.shift()?.()
  }
}

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))

/**
 * Uploads one file as a raw streamed body (docs/design/02 section 1.3). It waits for a free upload slot,
 * renews the session once if the access cookie has lapsed (the file can simply be sent again), and tries
 * again when the server asks it to wait (429), honouring Retry-After (CR-051, CR-053).
 */
export async function uploadFile(file: File): Promise<UploadedFile> {
  const id = crypto.randomUUID()
  return withUploadSlot(async () => {
    let renewed = false
    for (let attempt = 0; ; attempt++) {
      const res = await fetch(`/api/v1/attachments/${id}`, {
        method: 'PUT',
        headers: {
          ...CLIENT_HEADER,
          'Content-Type': 'application/octet-stream',
          'X-Filename': encodeURIComponent(file.name),
          'X-Media-Type': file.type || 'application/octet-stream',
        },
        body: file,
      })
      if (res.ok) return (await res.json()) as UploadedFile
      if (res.status === 401 && !renewed) {
        renewed = true
        if (await refreshSession()) continue
      }
      if (res.status === 429 && attempt < 5) {
        const wait = Number(res.headers.get('Retry-After'))
        await sleep(Math.min(Number.isFinite(wait) && wait > 0 ? wait * 1000 : 1000 * 2 ** attempt, 15000))
        continue
      }
      const problem = (await res.json().catch(() => ({}))) as { code?: string }
      throw new ApiError(res.status, problem.code ?? 'error')
    }
  })
}

// ---- merge and split (CORE-N14) ---------------------------------------------------------------

function refreshNotes(qc: QueryClient) {
  void qc.invalidateQueries({ queryKey: ['inbox'] })
  void qc.invalidateQueries({ queryKey: ['board'] })
  void qc.invalidateQueries({ queryKey: ['reminders'] })
  void qc.invalidateQueries({ queryKey: ['share-links'] })
}

export function useMergeNotes() {
  const qc = useQueryClient()
  const { toast } = useToast()
  return useMutation({
    mutationFn: async (v: { target: string; source: string }) =>
      unwrap(await api.POST('/api/v1/notes/{id}/merge', { params: { path: { id: v.target } }, body: { source_id: v.source } })),
    onSuccess: () => {
      refreshNotes(qc)
      toast({ message: t('merge.done') })
    },
  })
}

export function useSplitPart() {
  const qc = useQueryClient()
  const { toast } = useToast()
  return useMutation({
    mutationFn: async (v: { noteId: string; partId: string }) =>
      unwrap(await api.POST('/api/v1/notes/{id}/parts/{partId}/split', { params: { path: { id: v.noteId, partId: v.partId } } })),
    onSuccess: () => {
      refreshNotes(qc)
      toast({ message: t('merge.splitDone') })
    },
  })
}
